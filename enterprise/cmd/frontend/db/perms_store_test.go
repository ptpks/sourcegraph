package db

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RoaringBitmap/roaring"
	"github.com/gitchander/permutation"
	"github.com/google/go-cmp/cmp"
	"github.com/keegancsmith/sqlf"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/authz"
	"github.com/sourcegraph/sourcegraph/internal/extsvc"
	"golang.org/x/sync/errgroup"
)

func cleanupPermsTables(t *testing.T, s *PermsStore) {
	if t.Failed() {
		return
	}

	q := `TRUNCATE TABLE user_permissions, repo_permissions, user_pending_permissions, repo_pending_permissions;`
	if err := s.execute(context.Background(), sqlf.Sprintf(q)); err != nil {
		t.Fatal(err)
	}
}

func bitmapToArray(bm *roaring.Bitmap) []uint32 {
	if bm == nil {
		return nil
	}
	return bm.ToArray()
}

func toBitmap(ids ...uint32) *roaring.Bitmap {
	bm := roaring.NewBitmap()
	bm.AddMany(ids)
	return bm
}

var now = time.Now().Truncate(time.Microsecond).UnixNano()

func clock() time.Time {
	return time.Unix(0, atomic.LoadInt64(&now)).Truncate(time.Microsecond)
}

func testPermsStore_LoadUserPermissions(db *sql.DB) func(*testing.T) {
	return func(t *testing.T) {
		t.Run("no matching", func(t *testing.T) {
			s := NewPermsStore(db, clock)
			defer cleanupPermsTables(t, s)

			rp := &authz.RepoPermissions{
				RepoID:  1,
				Perm:    authz.Read,
				UserIDs: toBitmap(2),
			}
			if err := s.SetRepoPermissions(context.Background(), rp); err != nil {
				t.Fatal(err)
			}

			up := &authz.UserPermissions{
				UserID: 1,
				Perm:   authz.Read,
				Type:   authz.PermRepos,
			}
			err := s.LoadUserPermissions(context.Background(), up)
			if err != authz.ErrPermsNotFound {
				t.Fatalf("err: want %q but got %v", authz.ErrPermsNotFound, err)
			}
			equal(t, "IDs", 0, len(bitmapToArray(up.IDs)))
		})

		t.Run("found matching", func(t *testing.T) {
			s := NewPermsStore(db, clock)
			defer cleanupPermsTables(t, s)

			rp := &authz.RepoPermissions{
				RepoID:  1,
				Perm:    authz.Read,
				UserIDs: toBitmap(2),
			}
			if err := s.SetRepoPermissions(context.Background(), rp); err != nil {
				t.Fatal(err)
			}

			up := &authz.UserPermissions{
				UserID: 2,
				Perm:   authz.Read,
				Type:   authz.PermRepos,
			}
			if err := s.LoadUserPermissions(context.Background(), up); err != nil {
				t.Fatal(err)
			}
			equal(t, "IDs", []uint32{1}, bitmapToArray(up.IDs))
			equal(t, "UpdatedAt", now, up.UpdatedAt.UnixNano())
		})

		t.Run("add and change", func(t *testing.T) {
			s := NewPermsStore(db, clock)
			defer cleanupPermsTables(t, s)

			rp := &authz.RepoPermissions{
				RepoID:  1,
				Perm:    authz.Read,
				UserIDs: toBitmap(1, 2),
			}
			if err := s.SetRepoPermissions(context.Background(), rp); err != nil {
				t.Fatal(err)
			}

			rp = &authz.RepoPermissions{
				RepoID:  1,
				Perm:    authz.Read,
				UserIDs: toBitmap(2, 3),
			}
			if err := s.SetRepoPermissions(context.Background(), rp); err != nil {
				t.Fatal(err)
			}

			up1 := &authz.UserPermissions{
				UserID: 1,
				Perm:   authz.Read,
				Type:   authz.PermRepos,
			}
			if err := s.LoadUserPermissions(context.Background(), up1); err != nil {
				t.Fatal(err)
			}
			equal(t, "IDs", 0, len(bitmapToArray(up1.IDs)))

			up2 := &authz.UserPermissions{
				UserID: 2,
				Perm:   authz.Read,
				Type:   authz.PermRepos,
			}
			if err := s.LoadUserPermissions(context.Background(), up2); err != nil {
				t.Fatal(err)
			}
			equal(t, "IDs", []uint32{1}, bitmapToArray(up2.IDs))
			equal(t, "UpdatedAt", now, up2.UpdatedAt.UnixNano())

			up3 := &authz.UserPermissions{
				UserID: 3,
				Perm:   authz.Read,
				Type:   authz.PermRepos,
			}
			if err := s.LoadUserPermissions(context.Background(), up3); err != nil {
				t.Fatal(err)
			}
			equal(t, "IDs", []uint32{1}, bitmapToArray(up3.IDs))
			equal(t, "UpdatedAt", now, up3.UpdatedAt.UnixNano())
		})
	}
}

func testPermsStore_LoadRepoPermissions(db *sql.DB) func(*testing.T) {
	return func(t *testing.T) {
		t.Run("no matching", func(t *testing.T) {
			s := NewPermsStore(db, time.Now)
			defer cleanupPermsTables(t, s)

			rp := &authz.RepoPermissions{
				RepoID:  1,
				Perm:    authz.Read,
				UserIDs: toBitmap(2),
			}
			if err := s.SetRepoPermissions(context.Background(), rp); err != nil {
				t.Fatal(err)
			}

			rp = &authz.RepoPermissions{
				RepoID: 2,
				Perm:   authz.Read,
			}
			err := s.LoadRepoPermissions(context.Background(), rp)
			if err != authz.ErrPermsNotFound {
				t.Fatalf("err: want %q but got %q", authz.ErrPermsNotFound, err)
			}
			equal(t, "rp.UserIDs", 0, len(bitmapToArray(rp.UserIDs)))
		})

		t.Run("found matching", func(t *testing.T) {
			s := NewPermsStore(db, time.Now)
			defer cleanupPermsTables(t, s)

			rp := &authz.RepoPermissions{
				RepoID:  1,
				Perm:    authz.Read,
				UserIDs: toBitmap(2),
			}
			if err := s.SetRepoPermissions(context.Background(), rp); err != nil {
				t.Fatal(err)
			}

			rp = &authz.RepoPermissions{
				RepoID: 1,
				Perm:   authz.Read,
			}
			if err := s.LoadRepoPermissions(context.Background(), rp); err != nil {
				t.Fatal(err)
			}
			equal(t, "rp.UserIDs", []uint32{2}, bitmapToArray(rp.UserIDs))
		})
	}
}

func checkRegularPermsTable(s *PermsStore, sql string, expects map[int32][]uint32) error {
	rows, err := s.db.QueryContext(context.Background(), sql)
	if err != nil {
		return err
	}

	for rows.Next() {
		var id int32
		var ids []byte
		if err = rows.Scan(&id, &ids); err != nil {
			return err
		}

		bm := roaring.NewBitmap()
		if err = bm.UnmarshalBinary(ids); err != nil {
			return err
		}

		objIDs := bitmapToArray(bm)
		if expects[id] == nil {
			return fmt.Errorf("unexpected row in table: (id: %v) -> (ids: %v)", id, objIDs)
		}

		have := fmt.Sprintf("%v", objIDs)
		want := fmt.Sprintf("%v", expects[id])
		if have != want {
			return fmt.Errorf("key %v: want %q but got %q", id, want, have)
		}

		delete(expects, id)
	}

	if err = rows.Close(); err != nil {
		return err
	}

	if len(expects) > 0 {
		return fmt.Errorf("missing rows from table: %v", expects)
	}

	return nil
}

func testPermsStore_SetUserPermissions(db *sql.DB) func(*testing.T) {
	tests := []struct {
		name            string
		updates         []*authz.UserPermissions
		expectUserPerms map[int32][]uint32 // user_id -> object_ids
		expectRepoPerms map[int32][]uint32 // repo_id -> user_ids
	}{
		{
			name: "empty",
			updates: []*authz.UserPermissions{
				{
					UserID: 1,
					Perm:   authz.Read,
				},
			},
		},
		{
			name: "add",
			updates: []*authz.UserPermissions{
				{
					UserID: 1,
					Perm:   authz.Read,
					IDs:    toBitmap(1),
				}, {
					UserID: 2,
					Perm:   authz.Read,
					IDs:    toBitmap(1, 2),
				}, {
					UserID: 3,
					Perm:   authz.Read,
					IDs:    toBitmap(3, 4),
				},
			},
			expectUserPerms: map[int32][]uint32{
				1: {1},
				2: {1, 2},
				3: {3, 4},
			},
			expectRepoPerms: map[int32][]uint32{
				1: {1, 2},
				2: {2},
				3: {3},
				4: {3},
			},
		},
		{
			name: "add and update",
			updates: []*authz.UserPermissions{
				{
					UserID: 1,
					Perm:   authz.Read,
					IDs:    toBitmap(1),
				}, {
					UserID: 1,
					Perm:   authz.Read,
					IDs:    toBitmap(2, 3),
				}, {
					UserID: 2,
					Perm:   authz.Read,
					IDs:    toBitmap(1, 2),
				}, {
					UserID: 2,
					Perm:   authz.Read,
					IDs:    toBitmap(1, 3),
				},
			},
			expectUserPerms: map[int32][]uint32{
				1: {2, 3},
				2: {1, 3},
			},
			expectRepoPerms: map[int32][]uint32{
				1: {2},
				2: {1},
				3: {1, 2},
			},
		},
		{
			name: "add and clear",
			updates: []*authz.UserPermissions{
				{
					UserID: 1,
					Perm:   authz.Read,
					IDs:    toBitmap(1, 2, 3),
				}, {
					UserID: 1,
					Perm:   authz.Read,
					IDs:    toBitmap(),
				},
			},
			expectUserPerms: map[int32][]uint32{
				1: {},
			},
			expectRepoPerms: map[int32][]uint32{
				1: {},
				2: {},
				3: {},
			},
		},
	}

	return func(t *testing.T) {
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				s := NewPermsStore(db, clock)
				defer cleanupPermsTables(t, s)

				for _, p := range test.updates {
					const numOps = 30
					g, ctx := errgroup.WithContext(context.Background())
					for i := 0; i < numOps; i++ {
						g.Go(func() error {
							tmp := &authz.UserPermissions{
								UserID:    p.UserID,
								Perm:      p.Perm,
								UpdatedAt: p.UpdatedAt,
							}
							if p.IDs != nil {
								tmp.IDs = p.IDs.Clone()
							}
							return s.SetUserPermissions(ctx, tmp)
						})
					}
					if err := g.Wait(); err != nil {
						t.Fatal(err)
					}
				}

				err := checkRegularPermsTable(s, `SELECT user_id, object_ids FROM user_permissions`, test.expectUserPerms)
				if err != nil {
					t.Fatal("user_permissions:", err)
				}

				err = checkRegularPermsTable(s, `SELECT repo_id, user_ids FROM repo_permissions`, test.expectRepoPerms)
				if err != nil {
					t.Fatal("repo_permissions:", err)
				}
			})
		}
	}
}

func testPermsStore_SetRepoPermissions(db *sql.DB) func(*testing.T) {
	tests := []struct {
		name            string
		updates         []*authz.RepoPermissions
		expectUserPerms map[int32][]uint32 // user_id -> object_ids
		expectRepoPerms map[int32][]uint32 // repo_id -> user_ids
	}{
		{
			name: "empty",
			updates: []*authz.RepoPermissions{
				{
					RepoID: 1,
					Perm:   authz.Read,
				},
			},
		},
		{
			name: "add",
			updates: []*authz.RepoPermissions{
				{
					RepoID:  1,
					Perm:    authz.Read,
					UserIDs: toBitmap(1),
				}, {
					RepoID:  2,
					Perm:    authz.Read,
					UserIDs: toBitmap(1, 2),
				}, {
					RepoID:  3,
					Perm:    authz.Read,
					UserIDs: toBitmap(3, 4),
				},
			},
			expectUserPerms: map[int32][]uint32{
				1: {1, 2},
				2: {2},
				3: {3},
				4: {3},
			},
			expectRepoPerms: map[int32][]uint32{
				1: {1},
				2: {1, 2},
				3: {3, 4},
			},
		},
		{
			name: "add and update",
			updates: []*authz.RepoPermissions{
				{
					RepoID:  1,
					Perm:    authz.Read,
					UserIDs: toBitmap(1),
				}, {
					RepoID:  1,
					Perm:    authz.Read,
					UserIDs: toBitmap(2, 3),
				}, {
					RepoID:  2,
					Perm:    authz.Read,
					UserIDs: toBitmap(1, 2),
				}, {
					RepoID:  2,
					Perm:    authz.Read,
					UserIDs: toBitmap(3, 4),
				},
			},
			expectUserPerms: map[int32][]uint32{
				1: {},
				2: {1},
				3: {1, 2},
				4: {2},
			},
			expectRepoPerms: map[int32][]uint32{
				1: {2, 3},
				2: {3, 4},
			},
		},
		{
			name: "add and clear",
			updates: []*authz.RepoPermissions{
				{
					RepoID:  1,
					Perm:    authz.Read,
					UserIDs: toBitmap(1, 2, 3),
				}, {
					RepoID:  1,
					Perm:    authz.Read,
					UserIDs: toBitmap(),
				},
			},
			expectUserPerms: map[int32][]uint32{
				1: {},
				2: {},
				3: {},
			},
			expectRepoPerms: map[int32][]uint32{
				1: {},
			},
		},
	}

	return func(t *testing.T) {
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				s := NewPermsStore(db, clock)
				defer cleanupPermsTables(t, s)

				for _, p := range test.updates {
					const numOps = 30
					g, ctx := errgroup.WithContext(context.Background())
					for i := 0; i < numOps; i++ {
						g.Go(func() error {
							tmp := &authz.RepoPermissions{
								RepoID:    p.RepoID,
								Perm:      p.Perm,
								UpdatedAt: p.UpdatedAt,
							}
							if p.UserIDs != nil {
								tmp.UserIDs = p.UserIDs.Clone()
							}
							return s.SetRepoPermissions(ctx, tmp)
						})
					}
					if err := g.Wait(); err != nil {
						t.Fatal(err)
					}
				}

				err := checkRegularPermsTable(s, `SELECT user_id, object_ids FROM user_permissions`, test.expectUserPerms)
				if err != nil {
					t.Fatal("user_permissions:", err)
				}

				err = checkRegularPermsTable(s, `SELECT repo_id, user_ids FROM repo_permissions`, test.expectRepoPerms)
				if err != nil {
					t.Fatal("repo_permissions:", err)
				}
			})
		}
	}
}

func testPermsStore_LoadUserPendingPermissions(db *sql.DB) func(*testing.T) {
	return func(t *testing.T) {
		t.Run("no matching", func(t *testing.T) {
			s := NewPermsStore(db, clock)
			defer cleanupPermsTables(t, s)

			accounts := &extsvc.ExternalAccounts{
				ServiceType: "sourcegraph",
				ServiceID:   "https://sourcegraph.com/",
				AccountIDs:  []string{"bob"},
			}
			rp := &authz.RepoPermissions{
				RepoID: 1,
				Perm:   authz.Read,
			}
			if err := s.SetRepoPendingPermissions(context.Background(), accounts, rp); err != nil {
				t.Fatal(err)
			}

			up := &authz.UserPendingPermissions{
				ServiceType: "sourcegraph",
				ServiceID:   "https://sourcegraph.com/",
				BindID:      "alice",
				Perm:        authz.Read,
				Type:        authz.PermRepos,
			}
			err := s.LoadUserPendingPermissions(context.Background(), up)
			if err != authz.ErrPermsNotFound {
				t.Fatalf("err: want %q but got %q", authz.ErrPermsNotFound, err)
			}
			equal(t, "IDs", 0, len(bitmapToArray(up.IDs)))
		})

		t.Run("found matching", func(t *testing.T) {
			s := NewPermsStore(db, clock)
			defer cleanupPermsTables(t, s)

			accounts := &extsvc.ExternalAccounts{
				ServiceType: "sourcegraph",
				ServiceID:   "https://sourcegraph.com/",
				AccountIDs:  []string{"alice"},
			}
			rp := &authz.RepoPermissions{
				RepoID: 1,
				Perm:   authz.Read,
			}
			if err := s.SetRepoPendingPermissions(context.Background(), accounts, rp); err != nil {
				t.Fatal(err)
			}

			up := &authz.UserPendingPermissions{
				ServiceType: "sourcegraph",
				ServiceID:   "https://sourcegraph.com/",
				BindID:      "alice",
				Perm:        authz.Read,
				Type:        authz.PermRepos,
			}
			if err := s.LoadUserPendingPermissions(context.Background(), up); err != nil {
				t.Fatal(err)
			}
			equal(t, "IDs", []uint32{1}, bitmapToArray(up.IDs))
			equal(t, "UpdatedAt", now, up.UpdatedAt.UnixNano())
		})

		t.Run("add and change", func(t *testing.T) {
			s := NewPermsStore(db, clock)
			defer cleanupPermsTables(t, s)

			accounts := &extsvc.ExternalAccounts{
				ServiceType: "sourcegraph",
				ServiceID:   "https://sourcegraph.com/",
				AccountIDs:  []string{"alice", "bob"},
			}
			rp := &authz.RepoPermissions{
				RepoID: 1,
				Perm:   authz.Read,
			}
			if err := s.SetRepoPendingPermissions(context.Background(), accounts, rp); err != nil {
				t.Fatal(err)
			}

			accounts.AccountIDs = []string{"bob", "cindy"}
			rp = &authz.RepoPermissions{
				RepoID: 1,
				Perm:   authz.Read,
			}
			if err := s.SetRepoPendingPermissions(context.Background(), accounts, rp); err != nil {
				t.Fatal(err)
			}

			up1 := &authz.UserPendingPermissions{
				ServiceType: "sourcegraph",
				ServiceID:   "https://sourcegraph.com/",
				BindID:      "alice",
				Perm:        authz.Read,
				Type:        authz.PermRepos,
			}
			if err := s.LoadUserPendingPermissions(context.Background(), up1); err != nil {
				t.Fatal(err)
			}
			equal(t, "IDs", 0, len(bitmapToArray(up1.IDs)))

			up2 := &authz.UserPendingPermissions{
				ServiceType: "sourcegraph",
				ServiceID:   "https://sourcegraph.com/",
				BindID:      "bob",
				Perm:        authz.Read,
				Type:        authz.PermRepos,
			}
			if err := s.LoadUserPendingPermissions(context.Background(), up2); err != nil {
				t.Fatal(err)
			}
			equal(t, "IDs", []uint32{1}, bitmapToArray(up2.IDs))
			equal(t, "UpdatedAt", now, up2.UpdatedAt.UnixNano())

			up3 := &authz.UserPendingPermissions{
				ServiceType: "sourcegraph",
				ServiceID:   "https://sourcegraph.com/",
				BindID:      "cindy",
				Perm:        authz.Read,
				Type:        authz.PermRepos,
			}
			if err := s.LoadUserPendingPermissions(context.Background(), up3); err != nil {
				t.Fatal(err)
			}
			equal(t, "IDs", []uint32{1}, bitmapToArray(up3.IDs))
			equal(t, "UpdatedAt", now, up3.UpdatedAt.UnixNano())
		})
	}
}

func checkUserPendingPermsTable(ctx context.Context, s *PermsStore, expects map[string][]uint32) (map[int32]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, bind_id, object_ids FROM user_pending_permissions`)
	if err != nil {
		return nil, err
	}

	// Collect id -> bind_id mappings for later use.
	bindIDs := make(map[int32]string)
	for rows.Next() {
		var id int32
		var bindID string
		var ids []byte
		if err := rows.Scan(&id, &bindID, &ids); err != nil {
			return nil, err
		}
		bindIDs[id] = bindID

		bm := roaring.NewBitmap()
		if err = bm.UnmarshalBinary(ids); err != nil {
			return nil, err
		}

		repoIDs := bitmapToArray(bm)
		if expects[bindID] == nil {
			return nil, fmt.Errorf("unexpected row in table: (bind_id: %v) -> (ids: %v)", bindID, repoIDs)
		}

		have := fmt.Sprintf("%v", repoIDs)
		want := fmt.Sprintf("%v", expects[bindID])
		if have != want {
			return nil, fmt.Errorf("bindID %q: want %q but got %q", bindID, want, have)
		}
		delete(expects, bindID)
	}

	if err = rows.Close(); err != nil {
		return nil, err
	}

	if len(expects) > 0 {
		return nil, fmt.Errorf("missing rows from table: %v", expects)
	}

	return bindIDs, nil
}

func checkRepoPendingPermsTable(ctx context.Context, s *PermsStore, bindIDs map[int32]string, expects map[int32][]string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, user_ids FROM repo_pending_permissions`)
	if err != nil {
		return err
	}

	for rows.Next() {
		var id int32
		var ids []byte
		if err := rows.Scan(&id, &ids); err != nil {
			return err
		}

		bm := roaring.NewBitmap()
		if err = bm.UnmarshalBinary(ids); err != nil {
			return err
		}

		userIDs := bitmapToArray(bm)
		if expects[id] == nil {
			return fmt.Errorf("unexpected row in table: (id: %v) -> (ids: %v)", id, userIDs)
		}

		haveBindIDs := make([]string, 0, len(userIDs))
		for _, userID := range userIDs {
			bindID, ok := bindIDs[int32(userID)]
			if !ok {
				continue
			}

			haveBindIDs = append(haveBindIDs, bindID)
		}
		sort.Strings(haveBindIDs)

		have := fmt.Sprintf("%v", haveBindIDs)
		want := fmt.Sprintf("%v", expects[id])
		if have != want {
			return fmt.Errorf("id %d: want %q but got %q", id, want, have)
		}
		delete(expects, id)
	}

	if err = rows.Close(); err != nil {
		return err
	}

	if len(expects) > 0 {
		return fmt.Errorf("missing rows from table: %v", expects)
	}

	return nil
}

func testPermsStore_SetRepoPendingPermissions(db *sql.DB) func(*testing.T) {
	type update struct {
		accounts *extsvc.ExternalAccounts
		perm     *authz.RepoPermissions
	}
	tests := []struct {
		name                   string
		updates                []update
		expectUserPendingPerms map[string][]uint32 // bind_id -> object_ids
		expectRepoPendingPerms map[int32][]string  // repo_id -> bind_ids
	}{
		{
			name: "empty",
			updates: []update{
				{
					accounts: &extsvc.ExternalAccounts{
						ServiceType: "sourcegraph",
						ServiceID:   "https://sourcegraph.com/",
						AccountIDs:  nil,
					},
					perm: &authz.RepoPermissions{
						RepoID: 1,
						Perm:   authz.Read,
					},
				},
			},
		},
		{
			name: "add",
			updates: []update{
				{
					accounts: &extsvc.ExternalAccounts{
						ServiceType: "sourcegraph",
						ServiceID:   "https://sourcegraph.com/",
						AccountIDs:  []string{"alice"},
					},
					perm: &authz.RepoPermissions{
						RepoID: 1,
						Perm:   authz.Read,
					},
				}, {
					accounts: &extsvc.ExternalAccounts{
						ServiceType: "sourcegraph",
						ServiceID:   "https://sourcegraph.com/",
						AccountIDs:  []string{"alice", "bob"},
					},
					perm: &authz.RepoPermissions{
						RepoID: 2,
						Perm:   authz.Read,
					},
				}, {
					accounts: &extsvc.ExternalAccounts{
						ServiceType: "sourcegraph",
						ServiceID:   "https://sourcegraph.com/",
						AccountIDs:  []string{"cindy", "david"},
					},
					perm: &authz.RepoPermissions{
						RepoID: 3,
						Perm:   authz.Read,
					},
				},
			},
			expectUserPendingPerms: map[string][]uint32{
				"alice": {1, 2},
				"bob":   {2},
				"cindy": {3},
				"david": {3},
			},
			expectRepoPendingPerms: map[int32][]string{
				1: {"alice"},
				2: {"alice", "bob"},
				3: {"cindy", "david"},
			},
		},
		{
			name: "add and update",
			updates: []update{
				{
					accounts: &extsvc.ExternalAccounts{
						ServiceType: "sourcegraph",
						ServiceID:   "https://sourcegraph.com/",
						AccountIDs:  []string{"alice", "bob"},
					},
					perm: &authz.RepoPermissions{
						RepoID: 1,
						Perm:   authz.Read,
					},
				}, {
					accounts: &extsvc.ExternalAccounts{
						ServiceType: "sourcegraph",
						ServiceID:   "https://sourcegraph.com/",
						AccountIDs:  []string{"bob", "cindy"},
					},
					perm: &authz.RepoPermissions{
						RepoID: 1,
						Perm:   authz.Read,
					},
				}, {
					accounts: &extsvc.ExternalAccounts{
						ServiceType: "sourcegraph",
						ServiceID:   "https://sourcegraph.com/",
						AccountIDs:  []string{"alice", "bob"},
					},
					perm: &authz.RepoPermissions{
						RepoID: 2,
						Perm:   authz.Read,
					},
				}, {
					accounts: &extsvc.ExternalAccounts{
						ServiceType: "sourcegraph",
						ServiceID:   "https://sourcegraph.com/",
						AccountIDs:  []string{"cindy", "david"},
					},
					perm: &authz.RepoPermissions{
						RepoID: 2,
						Perm:   authz.Read,
					},
				},
			},
			expectUserPendingPerms: map[string][]uint32{
				"alice": {},
				"bob":   {1},
				"cindy": {1, 2},
				"david": {2},
			},
			expectRepoPendingPerms: map[int32][]string{
				1: {"bob", "cindy"},
				2: {"cindy", "david"},
			},
		},
		{
			name: "add and clear",
			updates: []update{
				{
					accounts: &extsvc.ExternalAccounts{
						ServiceType: "sourcegraph",
						ServiceID:   "https://sourcegraph.com/",
						AccountIDs:  []string{"alice", "bob", "cindy"},
					},
					perm: &authz.RepoPermissions{
						RepoID: 1,
						Perm:   authz.Read,
					},
				}, {
					accounts: &extsvc.ExternalAccounts{
						ServiceType: "sourcegraph",
						ServiceID:   "https://sourcegraph.com/",
						AccountIDs:  []string{},
					},
					perm: &authz.RepoPermissions{
						RepoID: 1,
						Perm:   authz.Read,
					},
				},
			},
			expectUserPendingPerms: map[string][]uint32{
				"alice": {},
				"bob":   {},
				"cindy": {},
			},
			expectRepoPendingPerms: map[int32][]string{
				1: {},
			},
		},
	}

	return func(t *testing.T) {
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				s := NewPermsStore(db, clock)
				defer cleanupPermsTables(t, s)

				ctx := context.Background()

				for _, update := range test.updates {
					const numOps = 30
					g, ctx := errgroup.WithContext(ctx)
					for i := 0; i < numOps; i++ {
						g.Go(func() error {
							tmp := &authz.RepoPermissions{
								RepoID:    update.perm.RepoID,
								Perm:      update.perm.Perm,
								UpdatedAt: update.perm.UpdatedAt,
							}
							if update.perm.UserIDs != nil {
								tmp.UserIDs = update.perm.UserIDs.Clone()
							}
							return s.SetRepoPendingPermissions(ctx, update.accounts, tmp)
						})
					}
					if err := g.Wait(); err != nil {
						t.Fatal(err)
					}
				}

				// Query and check rows in "user_pending_permissions" table.
				bindIDs, err := checkUserPendingPermsTable(ctx, s, test.expectUserPendingPerms)
				if err != nil {
					t.Fatal(err)
				}

				// Query and check rows in "repo_pending_permissions" table.
				err = checkRepoPendingPermsTable(ctx, s, bindIDs, test.expectRepoPendingPerms)
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func testPermsStore_ListPendingUsers(db *sql.DB) func(*testing.T) {
	type update struct {
		accounts *extsvc.ExternalAccounts
		perm     *authz.RepoPermissions
	}
	tests := []struct {
		name               string
		updates            []update
		expectPendingUsers []string
	}{
		{
			name:               "no user with pending permissions",
			expectPendingUsers: nil,
		},
		{
			name: "has user with pending permissions",
			updates: []update{
				{
					accounts: &extsvc.ExternalAccounts{
						ServiceType: "sourcegraph",
						ServiceID:   "https://sourcegraph.com/",
						AccountIDs:  []string{"alice"},
					},
					perm: &authz.RepoPermissions{
						RepoID: 1,
						Perm:   authz.Read,
					},
				},
			},
			expectPendingUsers: []string{"alice"},
		},
		{
			name: "has user but with empty object_ids",
			updates: []update{
				{
					accounts: &extsvc.ExternalAccounts{
						ServiceType: "sourcegraph",
						ServiceID:   "https://sourcegraph.com/",
						AccountIDs:  []string{"bob@example.com"},
					},
					perm: &authz.RepoPermissions{
						RepoID: 1,
						Perm:   authz.Read,
					},
				}, {
					accounts: &extsvc.ExternalAccounts{
						ServiceType: "sourcegraph",
						ServiceID:   "https://sourcegraph.com/",
						AccountIDs:  nil,
					},
					perm: &authz.RepoPermissions{
						RepoID: 1,
						Perm:   authz.Read,
					},
				},
			},
			expectPendingUsers: nil,
		},
	}
	return func(t *testing.T) {
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				s := NewPermsStore(db, clock)
				defer cleanupPermsTables(t, s)

				ctx := context.Background()

				for _, update := range test.updates {
					tmp := &authz.RepoPermissions{
						RepoID:    update.perm.RepoID,
						Perm:      update.perm.Perm,
						UpdatedAt: update.perm.UpdatedAt,
					}
					if update.perm.UserIDs != nil {
						tmp.UserIDs = update.perm.UserIDs.Clone()
					}
					if err := s.SetRepoPendingPermissions(ctx, update.accounts, tmp); err != nil {
						t.Fatal(err)
					}
				}

				bindIDs, err := s.ListPendingUsers(ctx)
				if err != nil {
					t.Fatal(err)
				}
				equal(t, "bindIDs", test.expectPendingUsers, bindIDs)
			})
		}
	}
}

func testPermsStore_GrantPendingPermissions(db *sql.DB) func(*testing.T) {
	type pending struct {
		accounts *extsvc.ExternalAccounts
		perm     *authz.RepoPermissions
	}
	type update struct {
		regulars []*authz.RepoPermissions
		pendings []pending
	}
	type grant struct {
		userID int32
		perm   *authz.UserPendingPermissions
	}
	tests := []struct {
		name                   string
		updates                []update
		grants                 []grant
		expectUserPerms        map[int32][]uint32  // user_id -> object_ids
		expectRepoPerms        map[int32][]uint32  // repo_id -> user_ids
		expectUserPendingPerms map[string][]uint32 // bind_id -> object_ids
		expectRepoPendingPerms map[int32][]string  // repo_id -> bind_ids
	}{
		{
			name: "empty",
			grants: []grant{
				{
					userID: 1,
					perm: &authz.UserPendingPermissions{
						ServiceType: "sourcegraph",
						ServiceID:   "https://sourcegraph.com/",
						BindID:      "alice",
						Perm:        authz.Read,
						Type:        authz.PermRepos,
					},
				},
			},
		},
		{
			name: "no matching pending permissions",
			updates: []update{
				{
					regulars: []*authz.RepoPermissions{
						{
							RepoID:  1,
							Perm:    authz.Read,
							UserIDs: toBitmap(1),
						}, {
							RepoID:  2,
							Perm:    authz.Read,
							UserIDs: toBitmap(1, 2),
						},
					},
					pendings: []pending{
						{
							accounts: &extsvc.ExternalAccounts{
								ServiceType: "sourcegraph",
								ServiceID:   "https://sourcegraph.com/",
								AccountIDs:  []string{"alice"},
							},
							perm: &authz.RepoPermissions{
								RepoID: 1,
								Perm:   authz.Read,
							},
						}, {
							accounts: &extsvc.ExternalAccounts{
								ServiceType: "sourcegraph",
								ServiceID:   "https://sourcegraph.com/",
								AccountIDs:  []string{"bob"},
							},
							perm: &authz.RepoPermissions{
								RepoID: 2,
								Perm:   authz.Read,
							},
						},
					},
				},
			},
			grants: []grant{
				{
					userID: 1,
					perm: &authz.UserPendingPermissions{
						ServiceType: "sourcegraph",
						ServiceID:   "https://sourcegraph.com/",
						BindID:      "cindy",
						Perm:        authz.Read,
						Type:        authz.PermRepos,
					},
				},
			},
			expectUserPerms: map[int32][]uint32{
				1: {1, 2},
				2: {2},
			},
			expectRepoPerms: map[int32][]uint32{
				1: {1},
				2: {1, 2},
			},
			expectUserPendingPerms: map[string][]uint32{
				"alice": {1},
				"bob":   {2},
			},
			expectRepoPendingPerms: map[int32][]string{
				1: {"alice"},
				2: {"bob"},
			},
		},
		{
			name: "found matching pending permissions",
			updates: []update{
				{
					regulars: []*authz.RepoPermissions{
						{
							RepoID:  1,
							Perm:    authz.Read,
							UserIDs: toBitmap(1),
						}, {
							RepoID:  2,
							Perm:    authz.Read,
							UserIDs: toBitmap(1, 2),
						},
					},
					pendings: []pending{
						{
							accounts: &extsvc.ExternalAccounts{
								ServiceType: "sourcegraph",
								ServiceID:   "https://sourcegraph.com/",
								AccountIDs:  []string{"alice"},
							},
							perm: &authz.RepoPermissions{
								RepoID: 1,
								Perm:   authz.Read,
							},
						}, {
							accounts: &extsvc.ExternalAccounts{
								ServiceType: "sourcegraph",
								ServiceID:   "https://sourcegraph.com/",
								AccountIDs:  []string{"bob"},
							},
							perm: &authz.RepoPermissions{
								RepoID: 2,
								Perm:   authz.Read,
							},
						},
					},
				},
			},
			grants: []grant{
				{
					userID: 3,
					perm: &authz.UserPendingPermissions{
						ServiceType: "sourcegraph",
						ServiceID:   "https://sourcegraph.com/",
						BindID:      "alice",
						Perm:        authz.Read,
						Type:        authz.PermRepos,
					},
				},
			},
			expectUserPerms: map[int32][]uint32{
				1: {1, 2},
				2: {2},
				3: {1},
			},
			expectRepoPerms: map[int32][]uint32{
				1: {1, 3},
				2: {1, 2},
			},
			expectUserPendingPerms: map[string][]uint32{
				"bob": {2},
			},
			expectRepoPendingPerms: map[int32][]string{
				1: {},
				2: {"bob"},
			},
		},
		{
			name: "union matching pending permissions to same user with different emails",
			updates: []update{
				{
					regulars: []*authz.RepoPermissions{
						{
							RepoID:  1,
							Perm:    authz.Read,
							UserIDs: toBitmap(1),
						}, {
							RepoID:  2,
							Perm:    authz.Read,
							UserIDs: toBitmap(1, 2),
						},
					},
					pendings: []pending{
						{
							accounts: &extsvc.ExternalAccounts{
								ServiceType: "sourcegraph",
								ServiceID:   "https://sourcegraph.com/",
								AccountIDs:  []string{"alice@example.com"},
							},
							perm: &authz.RepoPermissions{
								RepoID: 1,
								Perm:   authz.Read,
							},
						}, {
							accounts: &extsvc.ExternalAccounts{
								ServiceType: "sourcegraph",
								ServiceID:   "https://sourcegraph.com/",
								AccountIDs:  []string{"alice2@example.com"},
							},
							perm: &authz.RepoPermissions{
								RepoID: 2,
								Perm:   authz.Read,
							},
						},
					},
				},
			},
			grants: []grant{
				{
					userID: 3,
					perm: &authz.UserPendingPermissions{
						ServiceType: "sourcegraph",
						ServiceID:   "https://sourcegraph.com/",
						BindID:      "alice@example.com",
						Perm:        authz.Read,
						Type:        authz.PermRepos,
					},
				}, {
					userID: 3,
					perm: &authz.UserPendingPermissions{
						ServiceType: "sourcegraph",
						ServiceID:   "https://sourcegraph.com/",
						BindID:      "alice2@example.com",
						Perm:        authz.Read,
						Type:        authz.PermRepos,
					},
				},
			},
			expectUserPerms: map[int32][]uint32{
				1: {1, 2},
				2: {2},
				3: {1, 2},
			},
			expectRepoPerms: map[int32][]uint32{
				1: {1, 3},
				2: {1, 2, 3},
			},
			expectUserPendingPerms: map[string][]uint32{},
			expectRepoPendingPerms: map[int32][]string{
				1: {},
				2: {},
			},
		},
	}
	return func(t *testing.T) {
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				s := NewPermsStore(db, clock)
				defer cleanupPermsTables(t, s)

				ctx := context.Background()

				for _, update := range test.updates {
					for _, p := range update.regulars {
						if err := s.SetRepoPermissions(ctx, p); err != nil {
							t.Fatal(err)
						}
					}
					for _, p := range update.pendings {
						if err := s.SetRepoPendingPermissions(ctx, p.accounts, p.perm); err != nil {
							t.Fatal(err)
						}
					}
				}

				for _, grant := range test.grants {
					err := s.GrantPendingPermissions(ctx, grant.userID, grant.perm)
					if err != nil {
						t.Fatal(err)
					}
				}

				err := checkRegularPermsTable(s, `SELECT user_id, object_ids FROM user_permissions`, test.expectUserPerms)
				if err != nil {
					t.Fatal("user_permissions:", err)
				}

				err = checkRegularPermsTable(s, `SELECT repo_id, user_ids FROM repo_permissions`, test.expectRepoPerms)
				if err != nil {
					t.Fatal("repo_permissions:", err)
				}

				// Query and check rows in "user_pending_permissions" table.
				bindIDs, err := checkUserPendingPermsTable(ctx, s, test.expectUserPendingPerms)
				if err != nil {
					t.Fatal("user_pending_permissions:", err)
				}

				// Query and check rows in "repo_pending_permissions" table.
				err = checkRepoPendingPermsTable(ctx, s, bindIDs, test.expectRepoPendingPerms)
				if err != nil {
					t.Fatal("repo_pending_permissions:", err)
				}
			})
		}
	}
}

func testPermsStore_DeleteAllUserPermissions(db *sql.DB) func(*testing.T) {
	return func(t *testing.T) {
		s := NewPermsStore(db, clock)
		defer cleanupPermsTables(t, s)

		ctx := context.Background()

		// Set permissions for user 1 and 2
		if err := s.SetRepoPermissions(ctx, &authz.RepoPermissions{
			RepoID:  1,
			Perm:    authz.Read,
			UserIDs: toBitmap(1, 2),
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetRepoPermissions(ctx, &authz.RepoPermissions{
			RepoID:  2,
			Perm:    authz.Read,
			UserIDs: toBitmap(1, 2),
		}); err != nil {
			t.Fatal(err)
		}

		// Remove all permissions for the user=1
		if err := s.DeleteAllUserPermissions(ctx, 1); err != nil {
			t.Fatal(err)
		}

		// Check user=1 should not have any permissions now
		err := s.LoadUserPermissions(ctx, &authz.UserPermissions{
			UserID: 1,
			Perm:   authz.Read,
			Type:   authz.PermRepos,
		})
		if err != authz.ErrPermsNotFound {
			t.Fatalf("err: want %q but got %v", authz.ErrPermsNotFound, err)
		}

		// Check user=2 shoud not be affected
		p := &authz.UserPermissions{
			UserID: 2,
			Perm:   authz.Read,
			Type:   authz.PermRepos,
		}
		err = s.LoadUserPermissions(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		equal(t, "p.IDs", []uint32{1, 2}, bitmapToArray(p.IDs))
	}
}

func testPermsStore_DeleteAllUserPendingPermissions(db *sql.DB) func(*testing.T) {
	return func(t *testing.T) {
		s := NewPermsStore(db, clock)
		defer cleanupPermsTables(t, s)

		ctx := context.Background()

		accounts := &extsvc.ExternalAccounts{
			ServiceType: "sourcegraph",
			ServiceID:   "https://sourcegraph.com/",
			AccountIDs:  []string{"alice", "bob"},
		}

		// Set pending permissions for alice and bob
		if err := s.SetRepoPendingPermissions(ctx, accounts, &authz.RepoPermissions{
			RepoID: 1,
			Perm:   authz.Read,
		}); err != nil {
			t.Fatal(err)
		}

		// Remove all pending permissions for alice
		accounts.AccountIDs = []string{"alice"}
		if err := s.DeleteAllUserPendingPermissions(ctx, accounts); err != nil {
			t.Fatal(err)
		}

		// Check alice should not have any pending permissions now
		err := s.LoadUserPendingPermissions(ctx, &authz.UserPendingPermissions{
			ServiceType: "sourcegraph",
			ServiceID:   "https://sourcegraph.com/",
			BindID:      "alice",
			Perm:        authz.Read,
			Type:        authz.PermRepos,
		})
		if err != authz.ErrPermsNotFound {
			t.Fatalf("err: want %q but got %v", authz.ErrPermsNotFound, err)
		}

		// Check bob shoud not be affected
		p := &authz.UserPendingPermissions{
			ServiceType: "sourcegraph",
			ServiceID:   "https://sourcegraph.com/",
			BindID:      "bob",
			Perm:        authz.Read,
			Type:        authz.PermRepos,
		}
		err = s.LoadUserPendingPermissions(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		equal(t, "p.IDs", []uint32{1}, bitmapToArray(p.IDs))
	}
}

func testPermsStore_DatabaseDeadlocks(db *sql.DB) func(*testing.T) {
	return func(t *testing.T) {
		s := NewPermsStore(db, time.Now)
		defer cleanupPermsTables(t, s)

		ctx := context.Background()

		setUserPermissions := func(ctx context.Context, t *testing.T) {
			if err := s.SetUserPermissions(ctx, &authz.UserPermissions{
				UserID: 1,
				Perm:   authz.Read,
				IDs:    toBitmap(1),
			}); err != nil {
				t.Fatal(err)
			}
		}
		setRepoPermissions := func(ctx context.Context, t *testing.T) {
			if err := s.SetRepoPermissions(ctx, &authz.RepoPermissions{
				RepoID:  1,
				Perm:    authz.Read,
				UserIDs: toBitmap(1),
			}); err != nil {
				t.Fatal(err)
			}
		}
		setRepoPendingPermissions := func(ctx context.Context, t *testing.T) {
			accounts := &extsvc.ExternalAccounts{
				ServiceType: "sourcegraph",
				ServiceID:   "https://sourcegraph.com/",
				AccountIDs:  []string{"alice"},
			}
			if err := s.SetRepoPendingPermissions(ctx, accounts, &authz.RepoPermissions{
				RepoID: 1,
				Perm:   authz.Read,
			}); err != nil {
				t.Fatal(err)
			}
		}
		grantPendingPermissions := func(ctx context.Context, t *testing.T) {
			if err := s.GrantPendingPermissions(ctx, 1, &authz.UserPendingPermissions{
				ServiceType: "sourcegraph",
				ServiceID:   "https://sourcegraph.com/",
				BindID:      "alice",
				Perm:        authz.Read,
				Type:        authz.PermRepos,
			}); err != nil {
				t.Fatal(err)
			}
		}

		// Ensure we've run all permutations of ordering of the 4 calls to avoid nondeterminism in
		// test coverage stats.
		funcs := []func(context.Context, *testing.T){
			setRepoPendingPermissions, grantPendingPermissions, setRepoPermissions, setUserPermissions,
		}
		permutated := permutation.New(permutation.MustAnySlice(funcs))
		for permutated.Next() {
			for _, f := range funcs {
				f(ctx, t)
			}
		}

		const numOps = 50
		var wg sync.WaitGroup
		wg.Add(4)
		go func() {
			defer wg.Done()
			for i := 0; i < numOps; i++ {
				setUserPermissions(ctx, t)
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < numOps; i++ {
				setRepoPermissions(ctx, t)
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < numOps; i++ {
				setRepoPendingPermissions(ctx, t)
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < numOps; i++ {
				grantPendingPermissions(ctx, t)
			}
		}()

		wg.Wait()
	}
}

func cleanupUsersTable(t *testing.T, s *PermsStore) {
	if t.Failed() {
		return
	}

	q := `TRUNCATE TABLE users RESTART IDENTITY CASCADE;`
	if err := s.execute(context.Background(), sqlf.Sprintf(q)); err != nil {
		t.Fatal(err)
	}
}

func testPermsStore_ListExternalAccounts(db *sql.DB) func(*testing.T) {
	return func(t *testing.T) {
		s := NewPermsStore(db, time.Now)
		defer cleanupUsersTable(t, s)

		ctx := context.Background()

		// Set up test users and external accounts
		extSQL := `
INSERT INTO user_external_accounts(user_id, service_type, service_id, account_id, client_id, created_at, updated_at)
	VALUES(%s, %s, %s, %s, %s, %s, %s)
`
		qs := []*sqlf.Query{
			sqlf.Sprintf(`INSERT INTO users(username) VALUES('alice')`), // ID=1
			sqlf.Sprintf(`INSERT INTO users(username) VALUES('bob')`),   // ID=2

			sqlf.Sprintf(extSQL, 1, "gitlab", "https://gitlab.com/", "alice_gitlab", "alice_gitlab_client_id", clock(), clock()), // ID=1
			sqlf.Sprintf(extSQL, 1, "github", "https://github.com/", "alice_github", "alice_github_client_id", clock(), clock()), // ID=2
			sqlf.Sprintf(extSQL, 2, "gitlab", "https://gitlab.com/", "bob_gitlab", "bob_gitlab_client_id", clock(), clock()),     // ID=3
		}
		for _, q := range qs {
			if err := s.execute(ctx, q); err != nil {
				t.Fatal(err)
			}
		}

		{
			// Check external accounts for "alice"
			accounts, err := s.ListExternalAccounts(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}

			expAccounts := []*extsvc.ExternalAccount{
				{
					ID:     1,
					UserID: 1,
					ExternalAccountSpec: extsvc.ExternalAccountSpec{
						ServiceType: "gitlab",
						ServiceID:   "https://gitlab.com/",
						AccountID:   "alice_gitlab",
						ClientID:    "alice_gitlab_client_id",
					},
					CreatedAt: clock(),
					UpdatedAt: clock(),
				},
				{
					ID:     2,
					UserID: 1,
					ExternalAccountSpec: extsvc.ExternalAccountSpec{
						ServiceType: "github",
						ServiceID:   "https://github.com/",
						AccountID:   "alice_github",
						ClientID:    "alice_github_client_id",
					},
					CreatedAt: clock(),
					UpdatedAt: clock(),
				},
			}
			if diff := cmp.Diff(expAccounts, accounts); diff != "" {
				t.Fatalf(diff)
			}
		}

		{
			// Check external accounts for "bob"
			accounts, err := s.ListExternalAccounts(ctx, 2)
			if err != nil {
				t.Fatal(err)
			}

			expAccounts := []*extsvc.ExternalAccount{
				{
					ID:     3,
					UserID: 2,
					ExternalAccountSpec: extsvc.ExternalAccountSpec{
						ServiceType: "gitlab",
						ServiceID:   "https://gitlab.com/",
						AccountID:   "bob_gitlab",
						ClientID:    "bob_gitlab_client_id",
					},
					CreatedAt: clock(),
					UpdatedAt: clock(),
				},
			}
			if diff := cmp.Diff(expAccounts, accounts); diff != "" {
				t.Fatalf(diff)
			}
		}
	}
}

func testPermsStore_GetUserIDsByExternalAccounts(db *sql.DB) func(t *testing.T) {
	return func(t *testing.T) {
		s := NewPermsStore(db, time.Now)
		defer cleanupUsersTable(t, s)

		ctx := context.Background()

		// Set up test users and external accounts
		extSQL := `
INSERT INTO user_external_accounts(user_id, service_type, service_id, account_id, client_id, created_at, updated_at)
	VALUES(%s, %s, %s, %s, %s, %s, %s)
`
		qs := []*sqlf.Query{
			sqlf.Sprintf(`INSERT INTO users(username) VALUES('alice')`), // ID=1
			sqlf.Sprintf(`INSERT INTO users(username) VALUES('bob')`),   // ID=2
			sqlf.Sprintf(`INSERT INTO users(username) VALUES('cindy')`), // ID=3

			sqlf.Sprintf(extSQL, 1, "gitlab", "https://gitlab.com/", "alice_gitlab", "alice_gitlab_client_id", clock(), clock()), // ID=1
			sqlf.Sprintf(extSQL, 1, "github", "https://github.com/", "alice_github", "alice_github_client_id", clock(), clock()), // ID=2
			sqlf.Sprintf(extSQL, 2, "gitlab", "https://gitlab.com/", "bob_gitlab", "bob_gitlab_client_id", clock(), clock()),     // ID=3
			sqlf.Sprintf(extSQL, 3, "gitlab", "https://gitlab.com/", "cindy_gitlab", "cindy_gitlab_client_id", clock(), clock()), // ID=4
		}
		for _, q := range qs {
			if err := s.execute(ctx, q); err != nil {
				t.Fatal(err)
			}
		}

		accounts := &extsvc.ExternalAccounts{
			ServiceType: "gitlab",
			ServiceID:   "https://gitlab.com/",
			AccountIDs:  []string{"alice_gitlab", "bob_gitlab", "david_gitlab"},
		}
		userIDs, err := s.GetUserIDsByExternalAccounts(ctx, accounts)
		if err != nil {
			t.Fatal(err)
		}

		if len(userIDs) != 2 {
			t.Fatalf("len(userIDs): want 2 but got %v", userIDs)
		}

		if userIDs["alice_gitlab"] != 1 {
			t.Fatalf(`userIDs["alice_gitlab"]: want 1 but got %d`, userIDs["alice_gitlab"])
		} else if userIDs["bob_gitlab"] != 2 {
			t.Fatalf(`userIDs["bob_gitlab"]: want 2 but got %d`, userIDs["bob_gitlab"])
		}
	}
}
