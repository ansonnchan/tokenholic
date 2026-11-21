package migrations

import (
	"strings"
	"testing"
)

func TestAllReturnsContiguousImmutablePrivacySafeMigrations(t *testing.T) {
	t.Parallel()
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"initialize_server", "private_groups", "sync_v2_usage_buckets", "pricing_evidence", "global_board_opt_in", "profiles_and_group_visibility"}
	if len(migrations) != len(wantNames) {
		t.Fatalf("All() returned %d migrations, want %d", len(migrations), len(wantNames))
	}
	for index, name := range wantNames {
		if migrations[index].Version != index+1 || migrations[index].Name != name {
			t.Fatalf("migration %d = version %d %q, want %q", index+1, migrations[index].Version, migrations[index].Name, name)
		}
	}
	for _, migration := range migrations {
		if len(migration.Checksum) != 64 {
			t.Fatalf("migration %d checksum = %q", migration.Version, migration.Checksum)
		}
	}
	for _, prohibited := range []string{
		"prompt_text", "response_text", "source_code", "file_path", "project_name",
		"repository_name", "branch_name", "command_text", "provider_credential",
	} {
		for _, migration := range migrations {
			if strings.Contains(strings.ToLower(migration.SQL), prohibited) {
				t.Fatalf("migration %d contains prohibited field %q", migration.Version, prohibited)
			}
		}
	}
	migrations[0].SQL = "mutated"
	again, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if again[0].SQL == "mutated" {
		t.Fatal("All() returned mutable shared migration state")
	}
}
