(ns doxme.form.renderer-test
  (:require [clojure.test :refer [deftest testing is]]
            [doxme.form.renderer :as sut]))

;; ============================================================
;; Form Renderer Tests
;; BDD source: .ptsd/bdd/form-fsm.feature (UI rendering section)
;; ============================================================

;; --- Helpers ---

(defn- make-session
  "Create a minimal session for renderer tests."
  [{:keys [state step-idx data user-id]
    :or {data {} user-id 111222}}]
  {:state    state
   :step-idx step-idx
   :data     data
   :user-id  user-id})

(defn- keyboard-buttons
  "Extract all button action keywords from a keyboard."
  [keyboard]
  (->> keyboard
       flatten
       (map :action)
       set))

(defn- has-button? [keyboard action]
  (contains? (keyboard-buttons keyboard) action))

;; --- Render step: prompt text and progress ---

(deftest render-step-prompt-and-progress
  (testing "Render step shows prompt text and progress indicator"
    (let [session (make-session {:state :step/stack :step-idx 1})
          result  (sut/render-step session :en)]
      (is (string? (:text result)))
      (is (some? (:text result)))
      (is (re-find #"Step 2 of 5" (:text result))))))

;; --- Render first step: no back button ---

(deftest render-first-step-no-back
  (testing "Render first step does not show back button"
    (let [session (make-session {:state :step/salary :step-idx 0})
          result  (sut/render-step session :en)]
      (is (not (has-button? (:keyboard result) :back)))
      (is (has-button? (:keyboard result) :cancel)))))

;; --- Render optional field: skip button ---

(deftest render-optional-field-skip
  (testing "Render optional field shows skip button"
    (let [session (make-session {:state :step/location :step-idx 3})
          result  (sut/render-step session :en)]
      (is (has-button? (:keyboard result) :skip))
      (is (has-button? (:keyboard result) :back))
      (is (has-button? (:keyboard result) :cancel)))))

;; --- Render required field: no skip button ---

(deftest render-required-field-no-skip
  (testing "Render required field does not show skip button"
    (let [session (make-session {:state :step/salary :step-idx 0})
          result  (sut/render-step session :en)]
      (is (not (has-button? (:keyboard result) :skip))))))

;; --- Render confirm step: data summary and buttons ---

(deftest render-confirm-step
  (testing "Render confirm step shows data summary and confirm button"
    (let [data    {:salary   "200k USD"
                   :stack    "Rust, Go"
                   :role     "Staff Engineer"
                   :location "San Francisco, UTC-8"
                   :bio      "Systems programmer. Formerly at Google. Open source maintainer."}
          session (make-session {:state :confirm :step-idx 5 :data data})
          result  (sut/render-step session :en)]
      (is (re-find #"200k USD" (:text result)))
      (is (re-find #"Rust, Go" (:text result)))
      (is (re-find #"Staff Engineer" (:text result)))
      (is (has-button? (:keyboard result) :confirm))
      (is (has-button? (:keyboard result) :back))
      (is (has-button? (:keyboard result) :cancel)))))

;; --- i18n button labels ---

(deftest render-i18n-labels
  (testing "All button labels come from i18n - :ru language"
    (let [session (make-session {:state :step/stack :step-idx 1})
          result  (sut/render-step session :ru)
          labels  (->> (:keyboard result)
                       flatten
                       (map :label)
                       set)]
      ;; All labels should be non-nil strings (translated)
      (is (every? string? labels))
      (is (every? #(pos? (count %)) labels)))))

;; --- Progress indicator for different steps ---

(deftest render-progress-various-steps
  (testing "Progress indicator reflects current step position"
    (let [test-cases [[0 "Step 1 of 5"]
                      [1 "Step 2 of 5"]
                      [2 "Step 3 of 5"]
                      [3 "Step 4 of 5"]
                      [4 "Step 5 of 5"]]]
      (doseq [[idx expected-text] test-cases]
        (let [state   (keyword "step" (name (nth [:salary :stack :role :location :bio] idx)))
              session (make-session {:state state :step-idx idx})
              result  (sut/render-step session :en)]
          (is (re-find (re-pattern expected-text) (:text result))
              (str "Expected progress text '" expected-text "' at step-idx " idx)))))))

;; --- Keyboard structure ---

(deftest render-keyboard-structure
  (testing "Keyboard is a vector of rows (vectors of button maps)"
    (let [session (make-session {:state :step/stack :step-idx 1})
          result  (sut/render-step session :en)]
      (is (vector? (:keyboard result)))
      (is (every? vector? (:keyboard result)))
      (is (every? map? (flatten (:keyboard result)))))))

;; --- Mid-step buttons: back + cancel, no skip ---

(deftest render-mid-required-step-buttons
  (testing "Render required step (not first) shows back and cancel but no skip"
    (let [session (make-session {:state :step/stack :step-idx 1})
          result  (sut/render-step session :en)]
      (is (has-button? (:keyboard result) :back))
      (is (has-button? (:keyboard result) :cancel))
      (is (not (has-button? (:keyboard result) :skip))))))

;; --- Optional bio step ---

(deftest render-bio-step-skip
  (testing "Render bio step (optional) shows skip button"
    (let [session (make-session {:state :step/bio :step-idx 4})
          result  (sut/render-step session :en)]
      (is (has-button? (:keyboard result) :skip))
      (is (has-button? (:keyboard result) :back))
      (is (has-button? (:keyboard result) :cancel)))))
