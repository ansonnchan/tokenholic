package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	serverauth "github.com/ansonnchan/tokenholic/server/auth"
	servergroup "github.com/ansonnchan/tokenholic/server/group"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maximumGroupMembers = 100

// GroupRepository persists private groups and applies authorization in the
// same transactions that mutate membership or invite state.
type GroupRepository struct {
	pool *pgxpool.Pool
}

// NewGroupRepository constructs the PostgreSQL private-group repository.
func NewGroupRepository(pool *pgxpool.Pool) (*GroupRepository, error) {
	if pool == nil {
		return nil, errors.New("initialize group repository: pool is required")
	}
	return &GroupRepository{pool: pool}, nil
}

// CreateGroup creates a group and its single owner membership atomically.
func (r *GroupRepository) CreateGroup(ctx context.Context, principal serverauth.Principal, input servergroup.NewGroup) (group servergroup.Group, err error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return group, fmt.Errorf("begin group creation: %w", err)
	}
	defer rollbackOnError(ctx, tx, &err)
	if err = tx.QueryRow(ctx, `
		INSERT INTO groups(id, name, timezone, owner_user_id)
		VALUES ($1::uuid, $2, $3, $4::uuid)
		RETURNING id::text, name, timezone, visibility, created_at
	`, input.ID, input.Name, input.Timezone, principal.UserID).Scan(&group.ID, &group.Name, &group.Timezone, &group.Visibility, &group.CreatedAt); err != nil {
		return group, fmt.Errorf("create private group: %w", err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO group_memberships(group_id, user_id, role)
		VALUES ($1::uuid, $2::uuid, 'owner')
	`, input.ID, principal.UserID); err != nil {
		return group, fmt.Errorf("create group owner membership: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return group, fmt.Errorf("commit group creation: %w", err)
	}
	group.Role = servergroup.RoleOwner
	return group, nil
}

// ListGroups returns active memberships only.
func (r *GroupRepository) ListGroups(ctx context.Context, principal serverauth.Principal) ([]servergroup.Group, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT g.id::text, g.name, g.timezone, m.role, g.visibility, COALESCE(g.public_slug, ''), g.created_at
		FROM group_memberships m
		JOIN groups g ON g.id = m.group_id
		WHERE m.user_id = $1::uuid AND m.revoked_at IS NULL
		ORDER BY lower(g.name), g.id
	`, principal.UserID)
	if err != nil {
		return nil, fmt.Errorf("list private groups: %w", err)
	}
	defer rows.Close()
	groups := make([]servergroup.Group, 0)
	for rows.Next() {
		var group servergroup.Group
		if err := rows.Scan(&group.ID, &group.Name, &group.Timezone, &group.Role, &group.Visibility, &group.PublicSlug, &group.CreatedAt); err != nil {
			return nil, fmt.Errorf("decode private group: %w", err)
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate private groups: %w", err)
	}
	return groups, nil
}

// Group returns a membership-authorized snapshot of a group and its members.
func (r *GroupRepository) Group(ctx context.Context, principal serverauth.Principal, groupID string) (detail servergroup.Detail, err error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return detail, fmt.Errorf("begin group read: %w", err)
	}
	defer rollbackOnError(ctx, tx, &err)
	err = tx.QueryRow(ctx, `
		SELECT g.id::text, g.name, g.timezone, viewer.role, g.visibility, COALESCE(g.public_slug, ''), g.created_at
		FROM groups g
		JOIN group_memberships viewer ON viewer.group_id = g.id
		WHERE g.id = $1::uuid AND viewer.user_id = $2::uuid AND viewer.revoked_at IS NULL
	`, groupID, principal.UserID).Scan(
		&detail.Group.ID, &detail.Group.Name, &detail.Group.Timezone, &detail.Group.Role,
		&detail.Group.Visibility, &detail.Group.PublicSlug, &detail.Group.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return detail, servergroup.ErrNotFound
	}
	if err != nil {
		return detail, fmt.Errorf("authorize group read: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT m.user_id::text, u.display_name, m.role, m.joined_at
		FROM group_memberships m
		JOIN users u ON u.id = m.user_id
		WHERE m.group_id = $1::uuid AND m.revoked_at IS NULL AND u.deleted_at IS NULL
		ORDER BY CASE m.role WHEN 'owner' THEN 0 ELSE 1 END, lower(u.display_name), u.id
	`, groupID)
	if err != nil {
		return detail, fmt.Errorf("read group members: %w", err)
	}
	defer rows.Close()
	detail.Members = make([]servergroup.Member, 0)
	for rows.Next() {
		var member servergroup.Member
		if err := rows.Scan(&member.UserID, &member.DisplayName, &member.Role, &member.JoinedAt); err != nil {
			return detail, fmt.Errorf("decode group member: %w", err)
		}
		detail.Members = append(detail.Members, member)
	}
	if err := rows.Err(); err != nil {
		return detail, fmt.Errorf("iterate group members: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return detail, fmt.Errorf("commit group read: %w", err)
	}
	return detail, nil
}

// CreateInvite stores only a one-way code hash after locking and verifying the owner membership.
func (r *GroupRepository) CreateInvite(ctx context.Context, principal serverauth.Principal, invite servergroup.NewInvite) (err error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin group invite creation: %w", err)
	}
	defer rollbackOnError(ctx, tx, &err)
	var role servergroup.Role
	err = tx.QueryRow(ctx, `
		SELECT role FROM group_memberships
		WHERE group_id = $1::uuid AND user_id = $2::uuid AND revoked_at IS NULL
		FOR UPDATE
	`, invite.GroupID, principal.UserID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return servergroup.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("authorize group invite creation: %w", err)
	}
	if role != servergroup.RoleOwner {
		return servergroup.ErrForbidden
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO group_invites(id, group_id, created_by_user_id, code_hash, expires_at, max_uses)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6)
	`, invite.ID, invite.GroupID, principal.UserID, invite.CodeHash[:], invite.ExpiresAt, invite.MaxUses); err != nil {
		return fmt.Errorf("store group invite: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit group invite creation: %w", err)
	}
	return nil
}

// Join consumes an invite use and establishes the member role atomically.
func (r *GroupRepository) Join(ctx context.Context, principal serverauth.Principal, codeHash [32]byte, now time.Time) (group servergroup.Group, err error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return group, fmt.Errorf("begin group join: %w", err)
	}
	defer rollbackOnError(ctx, tx, &err)
	var inviteID string
	var expiresAt time.Time
	var maxUses, useCount int
	var revokedAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT i.id::text, g.id::text, g.name, g.timezone, g.visibility, COALESCE(g.public_slug, ''), g.created_at,
		       i.expires_at, i.max_uses, i.use_count, i.revoked_at
		FROM group_invites i
		JOIN groups g ON g.id = i.group_id
		WHERE i.code_hash = $1
		FOR UPDATE OF i
	`, codeHash[:]).Scan(
		&inviteID, &group.ID, &group.Name, &group.Timezone, &group.Visibility, &group.PublicSlug, &group.CreatedAt,
		&expiresAt, &maxUses, &useCount, &revokedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return group, servergroup.ErrInvalidInvite
	}
	if err != nil {
		return group, fmt.Errorf("read group invite: %w", err)
	}
	if revokedAt != nil || !expiresAt.After(now) || useCount >= maxUses {
		return group, servergroup.ErrInvalidInvite
	}
	// Different active invites can be consumed concurrently, so serialize joins
	// on the group before checking the fixed membership limit.
	var lockedGroupID string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM groups WHERE id = $1::uuid FOR UPDATE`, group.ID).Scan(&lockedGroupID); err != nil {
		return group, fmt.Errorf("lock group membership: %w", err)
	}
	var existingRole servergroup.Role
	var existingRevokedAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT role, revoked_at FROM group_memberships
		WHERE group_id = $1::uuid AND user_id = $2::uuid
		FOR UPDATE
	`, group.ID, principal.UserID).Scan(&existingRole, &existingRevokedAt)
	switch {
	case err == nil && existingRevokedAt == nil:
		return group, servergroup.ErrAlreadyMember
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return group, fmt.Errorf("read existing group membership: %w", err)
	}
	var memberCount int
	if err = tx.QueryRow(ctx, `SELECT COUNT(*) FROM group_memberships WHERE group_id = $1::uuid AND revoked_at IS NULL`, group.ID).Scan(&memberCount); err != nil {
		return group, fmt.Errorf("count group members: %w", err)
	}
	if memberCount >= maximumGroupMembers {
		return group, servergroup.ErrGroupFull
	}
	switch {
	case existingRevokedAt != nil:
		if _, err = tx.Exec(ctx, `
			UPDATE group_memberships
			SET role = 'member', joined_at = $3, revoked_at = NULL
			WHERE group_id = $1::uuid AND user_id = $2::uuid
		`, group.ID, principal.UserID, now); err != nil {
			return group, fmt.Errorf("restore group membership: %w", err)
		}
	default:
		if _, err = tx.Exec(ctx, `
			INSERT INTO group_memberships(group_id, user_id, role, joined_at)
			VALUES ($1::uuid, $2::uuid, 'member', $3)
		`, group.ID, principal.UserID, now); err != nil {
			return group, fmt.Errorf("create group membership: %w", err)
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE group_invites SET use_count = use_count + 1 WHERE id = $1::uuid`, inviteID); err != nil {
		return group, fmt.Errorf("consume group invite: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return group, fmt.Errorf("commit group join: %w", err)
	}
	group.Role = servergroup.RoleMember
	return group, nil
}

// Leave revokes only an active member role and preserves the owner invariant.
func (r *GroupRepository) Leave(ctx context.Context, principal serverauth.Principal, groupID string) (err error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin group leave: %w", err)
	}
	defer rollbackOnError(ctx, tx, &err)
	var role servergroup.Role
	err = tx.QueryRow(ctx, `
		SELECT role FROM group_memberships
		WHERE group_id = $1::uuid AND user_id = $2::uuid AND revoked_at IS NULL
		FOR UPDATE
	`, groupID, principal.UserID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return servergroup.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("authorize group leave: %w", err)
	}
	if role == servergroup.RoleOwner {
		return servergroup.ErrOwnerMustRemain
	}
	if _, err = tx.Exec(ctx, `
		UPDATE group_memberships SET revoked_at = CURRENT_TIMESTAMP
		WHERE group_id = $1::uuid AND user_id = $2::uuid
	`, groupID, principal.UserID); err != nil {
		return fmt.Errorf("leave private group: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit group leave: %w", err)
	}
	return nil
}

// RevokeMember removes a member after locking and verifying the owner role.
func (r *GroupRepository) RevokeMember(ctx context.Context, principal serverauth.Principal, groupID, userID string) (err error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin member revocation: %w", err)
	}
	defer rollbackOnError(ctx, tx, &err)
	var role servergroup.Role
	err = tx.QueryRow(ctx, `
		SELECT role FROM group_memberships
		WHERE group_id = $1::uuid AND user_id = $2::uuid AND revoked_at IS NULL
		FOR UPDATE
	`, groupID, principal.UserID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return servergroup.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("authorize member revocation: %w", err)
	}
	if role != servergroup.RoleOwner {
		return servergroup.ErrForbidden
	}
	if userID == principal.UserID {
		return servergroup.ErrOwnerMustRemain
	}
	tag, err := tx.Exec(ctx, `
		UPDATE group_memberships SET revoked_at = CURRENT_TIMESTAMP
		WHERE group_id = $1::uuid AND user_id = $2::uuid
		  AND role = 'member' AND revoked_at IS NULL
	`, groupID, userID)
	if err != nil {
		return fmt.Errorf("revoke group member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return servergroup.ErrNotFound
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit member revocation: %w", err)
	}
	return nil
}

// lockOwner locks the caller's active membership and requires the owner role.
// A caller who is not a member gets ErrNotFound, exactly as if the group did
// not exist; a member who is not the owner gets ErrForbidden.
func lockOwner(ctx context.Context, tx pgx.Tx, principal serverauth.Principal, groupID string) error {
	var role servergroup.Role
	err := tx.QueryRow(ctx, `
		SELECT role FROM group_memberships
		WHERE group_id = $1::uuid AND user_id = $2::uuid AND revoked_at IS NULL
		FOR UPDATE
	`, groupID, principal.UserID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return servergroup.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("authorize group owner operation: %w", err)
	}
	if role != servergroup.RoleOwner {
		return servergroup.ErrForbidden
	}
	return nil
}

// SetVisibility changes a group's public visibility after verifying the owner.
// The slug is assigned once and kept, so a board's address survives going
// private and back.
func (r *GroupRepository) SetVisibility(ctx context.Context, principal serverauth.Principal, groupID string, visibility servergroup.Visibility, slug string) (group servergroup.Group, err error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return group, fmt.Errorf("begin group visibility change: %w", err)
	}
	defer rollbackOnError(ctx, tx, &err)
	if err = lockOwner(ctx, tx, principal, groupID); err != nil {
		return group, err
	}
	if err = tx.QueryRow(ctx, `
		UPDATE groups SET
			visibility = $2,
			public_slug = CASE WHEN $2 = 'public' THEN COALESCE(public_slug, $3) ELSE public_slug END
		WHERE id = $1::uuid
		RETURNING id::text, name, timezone, visibility, COALESCE(public_slug, ''), created_at
	`, groupID, string(visibility), slug).Scan(&group.ID, &group.Name, &group.Timezone, &group.Visibility, &group.PublicSlug, &group.CreatedAt); err != nil {
		return group, fmt.Errorf("update group visibility: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return group, fmt.Errorf("commit group visibility change: %w", err)
	}
	group.Role = servergroup.RoleOwner
	return group, nil
}

// DeleteGroup removes a group after verifying the owner. Memberships and
// invitations go with it through their foreign keys; usage belongs to
// accounts and is untouched.
func (r *GroupRepository) DeleteGroup(ctx context.Context, principal serverauth.Principal, groupID string) (err error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin group deletion: %w", err)
	}
	defer rollbackOnError(ctx, tx, &err)
	if err = lockOwner(ctx, tx, principal, groupID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM groups WHERE id = $1::uuid`, groupID); err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit group deletion: %w", err)
	}
	return nil
}

// TransferOwnership makes another active member the owner. The previous
// owner is demoted first because a group can hold only one owner row.
func (r *GroupRepository) TransferOwnership(ctx context.Context, principal serverauth.Principal, groupID, login string) (err error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin ownership transfer: %w", err)
	}
	defer rollbackOnError(ctx, tx, &err)
	if err = lockOwner(ctx, tx, principal, groupID); err != nil {
		return err
	}
	var targetID string
	err = tx.QueryRow(ctx, `
		SELECT m.user_id::text
		FROM group_memberships m
		JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL
		JOIN github_identities gi ON gi.user_id = u.id
		WHERE m.group_id = $1::uuid AND m.revoked_at IS NULL AND lower(gi.login) = lower($2)
		ORDER BY gi.updated_at DESC
		LIMIT 1
		FOR UPDATE OF m
	`, groupID, login).Scan(&targetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return servergroup.ErrNotMember
	}
	if err != nil {
		return fmt.Errorf("find ownership transfer target: %w", err)
	}
	if targetID == principal.UserID {
		// Already the owner: nothing to change.
		return tx.Commit(ctx)
	}
	for _, step := range []struct {
		role, userID string
	}{{"member", principal.UserID}, {"owner", targetID}} {
		if _, err = tx.Exec(ctx, `UPDATE group_memberships SET role = $3 WHERE group_id = $1::uuid AND user_id = $2::uuid`, groupID, step.userID, step.role); err != nil {
			return fmt.Errorf("transfer group ownership: %w", err)
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE groups SET owner_user_id = $2::uuid WHERE id = $1::uuid`, groupID, targetID); err != nil {
		return fmt.Errorf("record group owner: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit ownership transfer: %w", err)
	}
	return nil
}

// InvitePreview names the group a usable invite joins. Every reason a code
// cannot be used is the same ErrInvalidInvite.
func (r *GroupRepository) InvitePreview(ctx context.Context, codeHash [32]byte, now time.Time) (servergroup.InvitePreview, error) {
	var preview servergroup.InvitePreview
	err := r.pool.QueryRow(ctx, `
		SELECT g.name, i.expires_at
		FROM group_invites i
		JOIN groups g ON g.id = i.group_id
		WHERE i.code_hash = $1 AND i.revoked_at IS NULL AND i.expires_at > $2 AND i.use_count < i.max_uses
	`, codeHash[:], now).Scan(&preview.GroupName, &preview.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return servergroup.InvitePreview{}, servergroup.ErrInvalidInvite
	}
	if err != nil {
		return servergroup.InvitePreview{}, fmt.Errorf("preview group invite: %w", err)
	}
	return preview, nil
}

func rollbackOnError(ctx context.Context, tx pgx.Tx, err *error) {
	if *err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
	}
}
