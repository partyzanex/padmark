-- +goose Up
-- direct_redirect is a per-link override of the server-wide --direct-redirect flag: NULL means
-- "inherit the server default" (the common case), 0/1 forces GET /r/{id} into interstitial/301
-- mode regardless of the flag. See domain.Link.EffectiveDirectRedirect.
ALTER TABLE links ADD COLUMN direct_redirect INTEGER;

-- +goose Down
ALTER TABLE links DROP COLUMN direct_redirect;
