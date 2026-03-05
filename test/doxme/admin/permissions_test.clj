(ns doxme.admin.permissions-test
  (:require [clojure.test :refer [deftest testing is are]]
            [doxme.admin.permissions :as sut]))

;; ──────────────────────────────────────────────
;; Fixtures — from admins.edn seed data
;; ──────────────────────────────────────────────

(def chat-id -1001234567890)

(def admins
  [{:xt/id "admin/111222333--1001234567890"
    :admin/user-id 111222333
    :admin/chat-id chat-id
    :admin/username "alexey_dev"
    :admin/first-name "Alexey"
    :admin/role :creator
    :admin/permissions #{:warn :mute :ban :unmute :unban :admin-add :admin-remove :warn-clear}
    :admin/granted-by nil
    :admin/granted-at #inst "2025-12-01T10:00:00.000Z"}
   {:xt/id "admin/222333444--1001234567890"
    :admin/user-id 222333444
    :admin/chat-id chat-id
    :admin/username "marina_fp"
    :admin/first-name "Marina"
    :admin/role :admin
    :admin/permissions #{:warn :mute :ban :unmute :unban :warn-clear}
    :admin/granted-by 111222333
    :admin/granted-at #inst "2025-12-15T14:00:00.000Z"}
   {:xt/id "admin/333444555--1001234567890"
    :admin/user-id 333444555
    :admin/chat-id chat-id
    :admin/username "dmi3_arch"
    :admin/first-name "Dmitry"
    :admin/role :admin
    :admin/permissions #{:warn :mute :unmute :warn-clear}
    :admin/granted-by 111222333
    :admin/granted-at #inst "2026-01-10T09:30:00.000Z"}])

(def telegram-admins-response
  {:ok true
   :result
   [{:user {:id 111222333
            :is_bot false
            :first_name "Alexey"
            :username "alexey_dev"}
     :status "creator"
     :is_anonymous false}
    {:user {:id 222333444
            :is_bot false
            :first_name "Marina"
            :username "marina_fp"}
     :status "administrator"
     :can_restrict_members true
     :can_delete_messages true
     :can_promote_members false}
    {:user {:id 987654321
            :is_bot true
            :first_name "DoxMe Bot"
            :username "doxme_bot"}
     :status "administrator"
     :can_restrict_members true
     :can_delete_messages true
     :can_promote_members false}]})

;; ──────────────────────────────────────────────
;; Admin detection from Telegram API response
;; ──────────────────────────────────────────────

(deftest parse-telegram-admins-test
  (testing "Scenario: Bot detects Telegram chat admins via getChatAdministrators API"
    (let [parsed (sut/parse-telegram-admins (:result telegram-admins-response))]
      (is (= "creator" (:status (get parsed 111222333)))
          "Admin cache should contain user 111222333 with status 'creator'")
      (is (= "administrator" (:status (get parsed 222333444)))
          "Admin cache should contain user 222333444 with status 'administrator'")
      (is (= "administrator" (:status (get parsed 987654321)))
          "Admin cache should contain bot 987654321 with status 'administrator'"))))

;; ──────────────────────────────────────────────
;; is-admin? checks
;; ──────────────────────────────────────────────

(deftest is-admin-test
  (testing "Creator is an admin"
    (is (true? (sut/is-admin? admins 111222333))
        "Creator should be recognized as admin"))

  (testing "Regular admin is an admin"
    (is (true? (sut/is-admin? admins 222333444))
        "Admin-role user should be recognized as admin"))

  (testing "Non-admin user is not an admin"
    (is (false? (sut/is-admin? admins 444555666))
        "Non-admin user should not be recognized as admin")))

;; ──────────────────────────────────────────────
;; is-creator? checks
;; ──────────────────────────────────────────────

(deftest is-creator-test
  (testing "Creator is identified correctly"
    (is (true? (sut/is-creator? admins 111222333))
        "User with :creator role should be detected"))

  (testing "Regular admin is not the creator"
    (is (false? (sut/is-creator? admins 222333444))
        "Admin-role user should not be detected as creator"))

  (testing "Non-admin is not the creator"
    (is (false? (sut/is-creator? admins 444555666)))))

;; ──────────────────────────────────────────────
;; has-permission? checks
;; ──────────────────────────────────────────────

(deftest has-permission-test
  (testing "Creator has admin-add permission"
    (is (true? (sut/has-permission? admins 111222333 :admin-add))
        "Creator should have :admin-add permission"))

  (testing "Regular admin does not have admin-add permission"
    (is (false? (sut/has-permission? admins 222333444 :admin-add))
        "Regular admin should not have :admin-add"))

  (testing "Regular admin has warn permission"
    (is (true? (sut/has-permission? admins 222333444 :warn))
        "Regular admin should have :warn"))

  (testing "Non-admin has no permissions"
    (is (false? (sut/has-permission? admins 444555666 :warn))
        "Non-admin should not have any permissions")))

;; ──────────────────────────────────────────────
;; /admin :list — admin list formatting
;; ──────────────────────────────────────────────

(deftest admin-list-test
  (testing "Scenario: /admin :list shows all admins to any user"
    (let [result (sut/format-admin-list admins)]
      (is (= (str "Chat Admins:\n"
                  "\n"
                  "1. @alexey_dev (creator)\n"
                  "2. @marina_fp (admin)\n"
                  "3. @dmi3_arch (admin)")
             result)))))

;; ──────────────────────────────────────────────
;; /admin :add — add bot-admin (creator-only)
;; ──────────────────────────────────────────────

(deftest admin-add-test
  (testing "Scenario: Creator adds a new bot-admin"
    (let [result (sut/check-admin-add admins 111222333 444555666 "pavel_clj")]
      (is (nil? (:error result))
          "Creator should be allowed to add bot-admins")
      (is (= "admin/444555666--1001234567890"
             (sut/admin-doc-id 444555666 chat-id))
          "Admin document ID should follow convention")))

  (testing "Scenario: Non-creator cannot add bot-admins"
    (let [result (sut/check-admin-add admins 222333444 444555666 "pavel_clj")]
      (is (= :creator-only (:error result)))
      (is (= "Only the chat creator can manage admins." (:message result))))))

;; ──────────────────────────────────────────────
;; /admin :remove — remove bot-admin (creator-only)
;; ──────────────────────────────────────────────

(deftest admin-remove-test
  (testing "Scenario: Creator removes a bot-admin"
    (let [result (sut/check-admin-remove admins 111222333 222333444 "marina_fp")]
      (is (nil? (:error result))
          "Creator should be allowed to remove bot-admins")))

  (testing "Scenario: Creator cannot remove themselves from admins"
    (let [result (sut/check-admin-remove admins 111222333 111222333 "alexey_dev")]
      (is (= :cannot-remove-creator (:error result)))
      (is (= "Cannot remove the chat creator from admins." (:message result))))))

;; ──────────────────────────────────────────────
;; Permission denied for non-admins
;; ──────────────────────────────────────────────

(deftest non-admin-rejected-test
  (testing "Scenario: Non-admin user is rejected from admin commands"
    (let [result (sut/check-admin-permission admins 444555666 :warn)]
      (is (= :permission-denied (:error result)))
      (is (= "You don't have permission to use this command." (:message result))))))

;; ──────────────────────────────────────────────
;; Edge cases
;; ──────────────────────────────────────────────

(deftest admin-commands-private-chat-test
  (testing "Scenario Outline: All admin commands are rejected in private chats"
    (are [command]
         (= {:error :groups-only
             :message "Admin commands are only available in group chats."}
            (sut/validate-chat-type "private"))

      "/warn @someone \"reason\""
      "/mute @someone 1h"
      "/ban @someone \"reason\""
      "/admin :add @someone")))

(deftest cannot-ban-admin-test
  (testing "Scenario: Admin tries to ban another admin"
    (let [result (sut/check-ban-target admins 111222333 222333444)]
      (is (= :cannot-ban-admin (:error result)))
      (is (= "Cannot ban an admin. Remove admin rights first." (:message result)))))

  (testing "Scenario: Regular admin tries to ban the creator"
    (let [result (sut/check-ban-target admins 222333444 111222333)]
      (is (= :cannot-ban-admin (:error result)))
      (is (= "Cannot ban an admin. Remove admin rights first." (:message result))))))

(deftest cannot-warn-self-test
  (testing "Scenario: Admin tries to warn themselves"
    (let [result (sut/check-warn-target admins 111222333 111222333 "alexey_dev"
                                        {:is-bot false :in-chat true})]
      (is (= :cannot-warn-self (:error result)))
      (is (= "Cannot warn yourself." (:message result))))))

(deftest cannot-warn-bot-test
  (testing "Scenario: Admin tries to warn the bot itself"
    (let [result (sut/check-warn-target admins 111222333 987654321 "doxme_bot"
                                        {:is-bot true :in-chat true})]
      (is (= :cannot-warn-bot (:error result)))
      (is (= "Cannot warn a bot." (:message result))))))

(deftest warn-non-member-test
  (testing "Scenario: Admin warns a user who is not in the chat"
    (let [result (sut/check-warn-target admins 111222333 nil "ghost_user"
                                        {:is-bot false :in-chat false})]
      (is (= :user-not-in-chat (:error result)))
      (is (= "User @ghost_user is not in this chat." (:message result))))))

(deftest bot-not-admin-test
  (testing "Scenario: Bot does not have admin rights in the chat"
    (let [result (sut/check-bot-permissions false)]
      (is (= :bot-not-admin (:error result)))
      (is (= "Bot needs admin rights with 'Restrict Members' permission to perform this action."
             (:message result))))))
