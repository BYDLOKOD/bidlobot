package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/domain/referral"
)

var (
	bktReferralServices     = []byte("referral_services")
	bktReferralServicesName = []byte("referral_services_name_idx")
	bktReferrals            = []byte("referrals")
)

// ReferralRepo implements referral.Store over bbolt. The three buckets
// are created lazily on first write so the repo works against any
// *bolt.DB, including ones opened before bolt.go registered them.
type ReferralRepo struct {
	db *bolt.DB
}

// NewReferralRepo constructs a ReferralRepo against the shared *bolt.DB.
func NewReferralRepo(db *bolt.DB) *ReferralRepo {
	return &ReferralRepo{db: db}
}

// Create implements referral.Store.Create.
func (r *ReferralRepo) Create(_ context.Context, absChatID int64, inSvc referral.Service, inRef referral.Referral) (*referral.Service, *referral.Referral, error) {
	now := time.Now().UTC()
	var outSvc *referral.Service
	var outRef *referral.Referral
	err := r.db.Update(func(tx *bolt.Tx) error {
		svcBkt, err := tx.CreateBucketIfNotExists(bktReferralServices)
		if err != nil {
			return fmt.Errorf("create referral_services: %w", err)
		}
		nameBkt, err := tx.CreateBucketIfNotExists(bktReferralServicesName)
		if err != nil {
			return fmt.Errorf("create referral_services_name_idx: %w", err)
		}
		refBkt, err := tx.CreateBucketIfNotExists(bktReferrals)
		if err != nil {
			return fmt.Errorf("create referrals: %w", err)
		}

		// Resolve the service: either load the selected ID, or insert a
		// new one only when no exact name index entry exists.
		service, err := resolveService(tx, svcBkt, nameBkt, absChatID, inSvc, now)
		if err != nil {
			return err
		}
		outSvc = service

		// Reject a second referral by the same owner/service and an exact
		// URL already present anywhere in the chat before allocating a
		// new referral ID.
		url := strings.TrimSpace(inRef.URL)
		if err := rejectDuplicates(refBkt, absChatID, service.ID, inRef.OwnerUserID, url); err != nil {
			return err
		}

		refID, err := refBkt.NextSequence()
		if err != nil {
			return fmt.Errorf("allocate referral id: %w", err)
		}
		stored := referral.Referral{
			ID:           refID,
			AbsChatID:    absChatID,
			ServiceID:    service.ID,
			OwnerUserID:  inRef.OwnerUserID,
			OwnerDisplay: inRef.OwnerDisplay,
			URL:          url,
			CreatedAt:    now,
		}
		buf, err := json.Marshal(stored)
		if err != nil {
			return fmt.Errorf("encode referral: %w", err)
		}
		if err := refBkt.Put(ReferralKey(absChatID, refID), buf); err != nil {
			return err
		}
		outRef = &stored
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return outSvc, outRef, nil
}

// List implements referral.Store.List.
func (r *ReferralRepo) List(_ context.Context, absChatID int64) ([]referral.Group, error) {
	var groups []referral.Group
	bySvc := make(map[uint64]int)
	err := r.db.View(func(tx *bolt.Tx) error {
		svcBkt := tx.Bucket(bktReferralServices)
		refBkt := tx.Bucket(bktReferrals)
		if svcBkt == nil {
			return nil
		}
		prefix := ReferralServicePrefix(absChatID)
		c := svcBkt.Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			var svc referral.Service
			if err := json.Unmarshal(v, &svc); err != nil {
				continue
			}
			bySvc[svc.ID] = len(groups)
			groups = append(groups, referral.Group{Service: svc, Referrals: []referral.Referral{}})
		}
		if refBkt == nil {
			return nil
		}
		rprefix := ReferralPrefix(absChatID)
		rc := refBkt.Cursor()
		for k, v := rc.Seek(rprefix); k != nil && bytes.HasPrefix(k, rprefix); k, v = rc.Next() {
			var ref referral.Referral
			if err := json.Unmarshal(v, &ref); err != nil {
				continue
			}
			idx, ok := bySvc[ref.ServiceID]
			if !ok {
				// Orphan referral whose service was pruned inconsistently.
				// Skip rather than crash; an admin /refreport can still
				// clean it up by ID.
				continue
			}
			groups[idx].Referrals = append(groups[idx].Referrals, ref)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(groups, func(i, j int) bool {
		return strings.ToLower(groups[i].Service.Name) < strings.ToLower(groups[j].Service.Name)
	})
	for i := range groups {
		sort.SliceStable(groups[i].Referrals, func(a, b int) bool {
			return groups[i].Referrals[a].ID < groups[i].Referrals[b].ID
		})
	}
	return groups, nil
}

// GetReferral implements referral.Store.GetReferral.
func (r *ReferralRepo) GetReferral(_ context.Context, absChatID int64, id uint64) (*referral.Referral, error) {
	var ref referral.Referral
	err := r.db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(bktReferrals)
		if bkt == nil {
			return referral.ErrNotFound
		}
		data := bkt.Get(ReferralKey(absChatID, id))
		if data == nil {
			return referral.ErrNotFound
		}
		if err := json.Unmarshal(data, &ref); err != nil {
			return fmt.Errorf("decode referral %d: %w", id, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &ref, nil
}

// DeleteReferral implements referral.Store.DeleteReferral. When the
// last referral under a service is removed, the service row and its
// name index entry are pruned so the catalog cannot accumulate empty
// categories.
func (r *ReferralRepo) DeleteReferral(_ context.Context, absChatID int64, id uint64) error {
	return r.db.Update(func(tx *bolt.Tx) error {
		refBkt := tx.Bucket(bktReferrals)
		if refBkt == nil {
			return referral.ErrNotFound
		}
		key := ReferralKey(absChatID, id)
		data := refBkt.Get(key)
		if data == nil {
			return referral.ErrNotFound
		}
		var ref referral.Referral
		if err := json.Unmarshal(data, &ref); err != nil {
			return fmt.Errorf("decode referral %d: %w", id, err)
		}
		if err := refBkt.Delete(key); err != nil {
			return err
		}

		// Re-scan this chat's referrals: if any other referral still
		// references the same service, leave the service alone.
		prefix := ReferralPrefix(absChatID)
		c := refBkt.Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			var other referral.Referral
			if err := json.Unmarshal(v, &other); err != nil {
				continue
			}
			if other.ServiceID == ref.ServiceID {
				return nil
			}
		}

		// No remaining referral: prune the service row and its name
		// index entry.
		svcBkt := tx.Bucket(bktReferralServices)
		if svcBkt != nil {
			if svcData := svcBkt.Get(ReferralServiceKey(absChatID, ref.ServiceID)); svcData != nil {
				var svc referral.Service
				if err := json.Unmarshal(svcData, &svc); err == nil {
					_ = svcBkt.Delete(ReferralServiceKey(absChatID, ref.ServiceID))
					nameBkt := tx.Bucket(bktReferralServicesName)
					if nameBkt != nil {
						_ = nameBkt.Delete(ReferralServiceNameKey(absChatID, svc.NameKey))
					}
				}
			}
		}
		return nil
	})
}

// resolveService loads an existing service by ID, or inserts a new one
// when no exact name index entry exists for the requested NameKey. A
// caller-supplied service template with ID==0 is the new-service flow;
// the canonical display name, effect, and NameKey come from the
// template.
func resolveService(tx *bolt.Tx, svcBkt, nameBkt *bolt.Bucket, absChatID int64, in referral.Service, now time.Time) (*referral.Service, error) {
	if in.ID != 0 {
		data := svcBkt.Get(ReferralServiceKey(absChatID, in.ID))
		if data == nil {
			return nil, referral.ErrNotFound
		}
		var svc referral.Service
		if err := json.Unmarshal(data, &svc); err != nil {
			return nil, fmt.Errorf("decode service %d: %w", in.ID, err)
		}
		svc.AbsChatID = absChatID
		return &svc, nil
	}

	name := strings.TrimSpace(in.Name)
	effect := strings.TrimSpace(in.Effect)
	nameKey := referral.NormalizeName(name)
	if nameKey == "" {
		return nil, fmt.Errorf("referral: empty service name")
	}

	// Exact name index lookup decides reuse vs. conflict. An existing
	// index entry means the catalog already has this category; the
	// caller must restart with the loaded service ID rather than create
	// a duplicate.
	idxKey := ReferralServiceNameKey(absChatID, nameKey)
	if existing := nameBkt.Get(idxKey); existing != nil {
		existingID := uint64(parseID(existing))
		data := svcBkt.Get(ReferralServiceKey(absChatID, existingID))
		if data == nil {
			// Stale index without a service row: overwrite below.
		} else {
			var svc referral.Service
			if err := json.Unmarshal(data, &svc); err != nil {
				return nil, fmt.Errorf("decode service %d: %w", existingID, err)
			}
			return nil, referral.ErrServiceExists
		}
	}

	id, err := svcBkt.NextSequence()
	if err != nil {
		return nil, fmt.Errorf("allocate service id: %w", err)
	}
	svc := referral.Service{
		ID:        id,
		AbsChatID: absChatID,
		Name:      name,
		Effect:    effect,
		NameKey:   nameKey,
		CreatedAt: now,
	}
	buf, err := json.Marshal(svc)
	if err != nil {
		return nil, fmt.Errorf("encode service: %w", err)
	}
	if err := svcBkt.Put(ReferralServiceKey(absChatID, id), buf); err != nil {
		return nil, err
	}
	idBytes := []byte(fmt.Sprintf("%020d", id))
	if err := nameBkt.Put(idxKey, idBytes); err != nil {
		return nil, err
	}
	return &svc, nil
}

// rejectDuplicates scans this chat's referrals once and rejects a
// second (owner, service) pair or an exact URL already present.
func rejectDuplicates(refBkt *bolt.Bucket, absChatID int64, serviceID uint64, ownerID int64, url string) error {
	prefix := ReferralPrefix(absChatID)
	c := refBkt.Cursor()
	for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
		var other referral.Referral
		if err := json.Unmarshal(v, &other); err != nil {
			continue
		}
		if other.ServiceID == serviceID && other.OwnerUserID == ownerID {
			return referral.ErrOwnerServiceExists
		}
		if url != "" && other.URL == url {
			return referral.ErrURLExists
		}
	}
	return nil
}
