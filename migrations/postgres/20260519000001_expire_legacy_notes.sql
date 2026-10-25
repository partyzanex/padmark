-- +goose Up
-- Intentionally a no-op. This migration used to expire every existing note so that plaintext
-- (pre-encryption, v0.1.6) rows were deleted on next access. Those rows are now kept: the
-- 20261023000001_legacy_notes migration tags them and `padmark-server encryption migrate`
-- converts them in place (hashed id, encrypted content, hashed edit code), so upgrading from
-- v0.1.6 preserves every note. Databases that already ran the old version keep its effect.

-- +goose Down
-- No-op.
