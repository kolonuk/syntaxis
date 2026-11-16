// SPDX-License-Identifier: GPL-2.0-or-later

package store

import "context"

// SetUserIDFloor makes the next chat user created get an id above floor. Tests that start real
// containers use it so their workspace volumes (named after the user id) can never be the same as
// the volumes of a real instance running on the same Docker host.
func (s *SQLite) SetUserIDFloor(ctx context.Context, floor int64) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO users (id,email,name,password_hash,totp_secret,totp_enabled,disabled,created_at,password_changed_at,remind_2fa_after) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		floor, "floor@example.com", "placeholder", "", []byte{}, 0, 1, 0, 0, 0); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id=?`, floor)
	return err
}
