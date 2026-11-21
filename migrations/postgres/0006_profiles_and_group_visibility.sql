-- Public visibility is opt-in everywhere. Every column below defaults to
-- private, so no existing or new profile or group is public until its owner
-- chooses that, and public reads must filter on these columns.

-- The avatar is GitHub's own image URL for the account, refreshed at login.
-- Fellow group members may see it; it is public only with a public profile.
ALTER TABLE github_identities ADD COLUMN avatar_url TEXT
    CHECK (avatar_url IS NULL OR (char_length(avatar_url) <= 512 AND avatar_url ~ '^https://avatars\.githubusercontent\.com/'));

ALTER TABLE users ADD COLUMN profile_visibility TEXT NOT NULL DEFAULT 'private'
    CHECK (profile_visibility IN ('private', 'public'));

-- A public profile is addressed by GitHub login, compared without case.
CREATE INDEX github_identities_login_lower ON github_identities(lower(login));

ALTER TABLE groups
    ADD COLUMN visibility TEXT NOT NULL DEFAULT 'private' CHECK (visibility IN ('private', 'public')),
    -- The slug names a public board in a URL. It is assigned when the owner
    -- first makes the group public and kept afterwards, so a link survives
    -- going private and back; while private it resolves to nothing.
    ADD COLUMN public_slug TEXT CHECK (public_slug IS NULL OR public_slug ~ '^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$'),
    ADD CHECK (visibility = 'private' OR public_slug IS NOT NULL);

CREATE UNIQUE INDEX groups_public_slug ON groups(public_slug) WHERE public_slug IS NOT NULL;

COMMENT ON TABLE groups IS 'Groups are visible through active membership. A public group also exposes a read-only board by slug, listing only members whose own profile is public.';
COMMENT ON COLUMN users.profile_visibility IS 'Public only after the account owner chose it. Public reads must treat a private profile exactly like a missing one.';
COMMENT ON COLUMN groups.visibility IS 'Public only after the group owner chose it. Public reads must treat a private group exactly like a missing one.';
