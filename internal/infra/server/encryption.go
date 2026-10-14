package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/uptrace/bun"
	"github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/infra/crypto"
	keyrepo "github.com/partyzanex/padmark/internal/infra/storage/contentkeys"
	"github.com/partyzanex/padmark/internal/usecases/contentkeys"
)

const (
	flagKeyID     = "key-id"
	flagKeyOutput = "output"
	flagDryRun    = "dry-run"
)

const (
	encryptionMigrate = "migrate"
	encryptionRewrap  = "rewrap"
	encryptionConvert = "convert"
)

func encryptionCommand() *cli.Command {
	flags := append(migrateFlags(), &cli.StringFlag{
		Name: FlagContentKeyring, Sources: cli.EnvVars(EnvContentKeyring), Usage: "Persisted content keyring JSON file",
	})

	items := []struct{ name, usage string }{
		{"keygen", "Generate a keyring in a new 0600 file"},
		{"key-add", "Add a random KEK to a new keyring file (does not activate/register it)"},
		{"key-activate", "Select an already registered KEK in a new keyring file"},
		{"key-remove", "Remove an unused, inactive KEK from a new keyring file"},
		{"register", "Register a KEK with this installation without overwriting existing checks"},
		{"status", "Verify keys and envelopes; print counts without changing the database"},
		{encryptionMigrate, "Wrap raw legacy ciphertext; resume an interrupted migration"},
		{encryptionRewrap, "Rotate DEK wrappers to the active KEK, keeping payloads unchanged"},
		{encryptionConvert, "Convert wrapped legacy records using one original slug per stdin line"},
	}

	commands := make([]*cli.Command, 0, len(items))
	for i := range items {
		item := &items[i]
		commands = append(commands, &cli.Command{ //nolint:exhaustruct_v5 // optional CLI hooks use their zero values
			Name: item.name, Usage: item.usage, Action: encryptionAction,
			Flags: []cli.Flag{
				&cli.StringFlag{Name: flagKeyID, Usage: "Public key ID (required for key operations)"},
				&cli.StringFlag{Name: flagKeyOutput, Usage: "New keyring filename; existing files are never overwritten"},
				&cli.BoolFlag{Name: flagDryRun, Usage: "Inspect migration, rewrap or conversion without database writes"},
			},
		})
	}

	return &cli.Command{ //nolint:exhaustruct_v5 // optional CLI hooks use their zero values
		Name: "encryption", Usage: "Manage content keys, migration and rotation",
		Description: "Apply schema migrations first with `padmark migrate`. Stop all servers and writers " +
			"before changing encryption state or retiring keys. Keep database and keyring backups. " +
			"Key files are written only to new paths; distribute them explicitly to every replica.",
		Flags: flags, Commands: commands,
	}
}

func newEncryptionManager(db *bun.DB, keyring *crypto.ContentKeyring) (*contentkeys.Manager, error) {
	notes, err := keyring.ForResource("note")
	if err != nil {
		return nil, encryptionError(err)
	}

	links, err := keyring.ForResource("link")
	if err != nil {
		return nil, encryptionError(err)
	}

	return contentkeys.New(keyrepo.New(db), keyring, notes, links), nil
}

func encryptionAction(ctx context.Context, cmd *cli.Command) error {
	if cmd.Bool(flagDryRun) && cmd.Name != encryptionMigrate &&
		cmd.Name != encryptionRewrap && cmd.Name != encryptionConvert {
		return errors.New("--dry-run is supported only by migrate, rewrap and convert; status is always read-only")
	}

	if cmd.Name == "keygen" {
		return generateKeyring(cmd)
	}

	keyring, err := crypto.LoadContentKeyring(cmd.String(FlagContentKeyring))
	if err != nil {
		return encryptionError(err)
	}

	if cmd.Name == "key-add" {
		err = keyring.AddKey(cmd.String(flagKeyID))
		if err != nil {
			return encryptionError(err)
		}

		return writeKeyring(cmd, keyring)
	}

	db, err := openDB(ctx, cmd.String(FlagStorage), cmd.String(FlagDSN))
	if err != nil {
		return encryptionError(err)
	}
	defer db.Close() //nolint:errcheck // all mutations commit before returning

	manager, err := newEncryptionManager(db, keyring)
	if err != nil {
		return encryptionError(err)
	}

	return runEncryptionOperation(ctx, cmd, db, keyring, manager)
}

func runEncryptionOperation(ctx context.Context, cmd *cli.Command, db *bun.DB,
	keyring *crypto.ContentKeyring, manager *contentkeys.Manager) error {
	var err error

	switch cmd.Name {
	case "register":
		return encryptionError(manager.Register(ctx, cmd.String(flagKeyID)))
	case "key-activate":
		reg, readErr := keyrepo.New(db).Registry(ctx)
		if readErr != nil {
			return encryptionError(readErr)
		}

		if reg == nil {
			return errors.New("initialize and register the key before activation")
		}

		keyring.Active = cmd.String(flagKeyID)

		manager, err = newEncryptionManager(db, keyring)
		if err != nil {
			return encryptionError(err)
		}

		err := manager.PrepareServe(ctx)
		if err != nil {
			return encryptionError(err)
		}

		return writeKeyring(cmd, keyring)
	case "key-remove":
		id := cmd.String(flagKeyID)
		if _, exists := keyring.Keys[id]; !exists {
			return errors.New("key ID not present in keyring")
		}

		err := manager.CanRemove(ctx, id)
		if err != nil {
			return encryptionError(err)
		}

		delete(keyring.Keys, id)

		return writeKeyring(cmd, keyring)
	default:
		return runEncryptionData(ctx, cmd, manager)
	}
}

func runEncryptionData(ctx context.Context, cmd *cli.Command, manager *contentkeys.Manager) error {
	var (
		report contentkeys.Report
		err    error
	)

	switch cmd.Name {
	case "status":
		report, err = manager.Status(ctx)
	case encryptionMigrate:
		report, err = manager.Migrate(ctx, cmd.Bool(flagDryRun))
	case encryptionRewrap:
		report, err = manager.Rewrap(ctx, cmd.Bool(flagDryRun))
	case encryptionConvert:
		input := cmd.Root().Reader
		if input == nil {
			input = os.Stdin
		}

		report, err = manager.ConvertSlugs(ctx, input, cmd.Bool(flagDryRun))
	default:
		return errors.New("unknown encryption operation")
	}

	output := cmd.Root().Writer
	if output == nil {
		output = os.Stdout
	}

	writeErr := json.NewEncoder(output).Encode(report)
	if writeErr != nil {
		return errors.Join(err, fmt.Errorf("write encryption report: %w", writeErr))
	}

	return encryptionError(err)
}

func generateKeyring(cmd *cli.Command) error {
	keyring := &crypto.ContentKeyring{Active: cmd.String(flagKeyID), Keys: make(map[string][]byte)}

	err := keyring.AddKey(keyring.Active)
	if err != nil {
		return encryptionError(err)
	}

	return writeKeyring(cmd, keyring)
}

func writeKeyring(cmd *cli.Command, keyring *crypto.ContentKeyring) error {
	path := cmd.String(flagKeyOutput)
	if path == "" {
		return errors.New("--output must name a new keyring file outside the repository")
	}

	writeErr := crypto.WriteContentKeyring(path, keyring)
	if writeErr != nil {
		return encryptionError(writeErr)
	}

	output := cmd.Root().Writer
	if output == nil {
		output = os.Stdout
	}

	_, err := io.WriteString(output, "Keyring written; distribute the file to all replicas and back it up separately.\n")
	if err != nil {
		return fmt.Errorf("write keyring result: %w", err)
	}

	return nil
}

func validateContentEncryption(ctx context.Context, db *bun.DB, keyring *crypto.ContentKeyring) error {
	manager, err := newEncryptionManager(db, keyring)
	if err != nil {
		return err
	}

	return encryptionError(manager.PrepareServe(ctx))
}
