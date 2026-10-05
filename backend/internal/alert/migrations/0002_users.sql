-- Subscriptions can belong to a signed-in user (NULL = created by an operator), and an
-- in-app notification is one the user reads in the app, so it records when it was read.
ALTER TABLE subscriptions ADD COLUMN user_id INTEGER;
ALTER TABLE notifications ADD COLUMN read_at INTEGER;

-- A user follows a location at most once (location NULL = every location).
CREATE UNIQUE INDEX subscriptions_one_per_user_location
    ON subscriptions (user_id, COALESCE(location_id, 0))
    WHERE user_id IS NOT NULL AND active = 1;

CREATE INDEX subscriptions_by_user ON subscriptions (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX notifications_by_subscription ON notifications (subscription_id, id DESC);
