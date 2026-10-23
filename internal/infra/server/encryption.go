package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

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
	flagCorrupt   = "corrupt"
)

const (
	encryptionMigrate = "migrate"
	encryptionRewrap  = "rewrap"
	encryptionConvert = "convert"
	encryptionPrune   = "prune"
)

func encryptionCommand() *cli.Command {
	flags := append(migrateFlags(), &cli.StringFlag{
		Name: FlagContentKeyring, Sources: cli.EnvVars(EnvContentKeyring),
		Usage: "Content keyring JSON file; sqlite defaults to <db dir>/padmark-keys.json (never created here)",
	}, allowSharedKeyringFlag())
	flags = append(flags, argon2Flags()...)

	items := []struct{ name, usage string }{
		{"keygen", "Generate a keyring in a new 0600 file"},
		{"key-add", "Add a random KEK to a new keyring file (does not activate/register it)"},
		{"key-activate", "Select an already registered KEK in a new keyring file"},
		{"key-remove", "Remove an unused, inactive KEK from a new keyring file"},
		{"register", "Register a KEK with this installation without overwriting existing checks"},
		{"status", "Verify keys and envelopes; print counts (plaintext = unconverted v0.1.6 notes) " +
			"without changing the database"},
		{encryptionMigrate, "Wrap raw legacy ciphertext and convert plaintext v0.1.6 notes in place " +
			"(hashed ID, encrypted content, hashed edit code); resume an interrupted migration"},
		{encryptionRewrap, "Rotate DEK wrappers to the active KEK, keeping payloads unchanged"},
		{encryptionConvert, "Convert wrapped legacy records using one original slug per stdin line"},
		{encryptionPrune, "Permanently remove malformed raw records; preview with --corrupt --dry-run"},
	}

	commands := make([]*cli.Command, 0, len(items))
	for i := range items {
		item := &items[i]

		operationFlags := []cli.Flag{
			&cli.StringFlag{Name: flagKeyID, Usage: "Public key ID (required for key operations)"},
			&cli.StringFlag{Name: flagKeyOutput, Usage: "New keyring filename; existing files are never overwritten"},
			&cli.BoolFlag{Name: flagDryRun, Usage: "Inspect migration, rewrap, conversion or pruning without writes"},
		}

		if item.name == encryptionPrune {
			operationFlags = append(operationFlags, &cli.BoolFlag{
				Name: flagCorrupt, Usage: "Explicitly select malformed raw records for permanent deletion",
			})
		}

		commands = append(commands, &cli.Command{ //nolint:exhaustruct_v5 // optional CLI hooks use their zero values
			Name: item.name, Usage: item.usage, Action: encryptionAction,
			Flags: operationFlags,
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

// newEncryptionManager builds the content-key manager. hasher hashes the plaintext edit codes
// of legacy (v0.1.6) notes converted by encryption migrate; serve never converts, but the
// manager always needs one.
func newEncryptionManager(
	db *bun.DB, keyring *crypto.ContentKeyring, hasher contentkeys.EditCodeHasher,
) (*contentkeys.Manager, error) {
	notes, err := keyring.ForResource("note")
	if err != nil {
		//nolint:wrapcheck // WrapError adds shared encryption context.
		return nil, contentkeys.WrapError(err)
	}

	links, err := keyring.ForResource("link")
	if err != nil {
		//nolint:wrapcheck // WrapError adds shared encryption context.
		return nil, contentkeys.WrapError(err)
	}

	return contentkeys.New(keyrepo.New(db), keyring, notes, links, hasher), nil
}

func validateEncryptionFlags(cmd *cli.Command) error {
	if cmd.Name == encryptionPrune && !cmd.Bool(flagCorrupt) {
		return errors.New("prune requires --corrupt; preview permanent raw-record deletion with --corrupt --dry-run")
	}

	if !cmd.Bool(flagDryRun) {
		return nil
	}

	switch cmd.Name {
	case encryptionMigrate, encryptionRewrap, encryptionConvert, encryptionPrune:
		return nil
	default:
		return errors.New("--dry-run is supported only by migrate, rewrap, convert and prune; status is always read-only")
	}
}

func encryptionAction(ctx context.Context, cmd *cli.Command) error {
	flagErr := validateEncryptionFlags(cmd)
	if flagErr != nil {
		return flagErr
	}

	if cmd.Name == "keygen" {
		return generateKeyring(cmd)
	}

	log := newLogger(DefaultLogLevel, DefaultLogFormat)

	keyring, err := loadToolKeyring(cmd.String(FlagContentKeyring), cmd.String(FlagStorage), cmd.String(FlagDSN),
		cmd.Bool(FlagAllowSharedKeyring), log)
	if err != nil {
		//nolint:wrapcheck // WrapError adds shared encryption context.
		return contentkeys.WrapError(err)
	}

	if cmd.Name == "key-add" {
		err = keyring.AddKey(cmd.String(flagKeyID))
		if err != nil {
			//nolint:wrapcheck // WrapError adds shared encryption context.
			return contentkeys.WrapError(err)
		}

		return writeKeyring(cmd, keyring)
	}

	argonErr := validateArgon2Config(cmd)
	if argonErr != nil {
		return argonErr
	}

	// Bound argon2id workspace before converted legacy edit codes are hashed.
	crypto.SetArgon2MemoryBudget(uint32(cmd.Int(FlagArgon2MemoryBudget))) //nolint:gosec // G115: bounded flag

	db, err := openDB(ctx, cmd.String(FlagStorage), cmd.String(FlagDSN), log)
	if err != nil {
		//nolint:wrapcheck // WrapError adds shared encryption context.
		return contentkeys.WrapError(err)
	}
	defer db.Close() //nolint:errcheck // all mutations commit before returning

	manager, err := newEncryptionManager(db, keyring, encryptionHasher(cmd))
	if err != nil {
		//nolint:wrapcheck // WrapError adds shared encryption context.
		return contentkeys.WrapError(err)
	}

	return runEncryptionOperation(ctx, cmd, db, keyring, manager)
}

func runEncryptionOperation(ctx context.Context, cmd *cli.Command, db *bun.DB,
	keyring *crypto.ContentKeyring, manager *contentkeys.Manager) error {
	var err error

	switch cmd.Name {
	case "register":
		//nolint:wrapcheck // WrapError adds shared encryption context.
		return contentkeys.WrapError(manager.Register(ctx, cmd.String(flagKeyID)))
	case "key-activate":
		reg, readErr := keyrepo.New(db).Registry(ctx)
		if readErr != nil {
			//nolint:wrapcheck // WrapError adds shared encryption context.
			return contentkeys.WrapError(readErr)
		}

		if reg == nil {
			return errors.New("initialize and register the key before activation")
		}

		keyring.Active = cmd.String(flagKeyID)

		manager, err = newEncryptionManager(db, keyring, encryptionHasher(cmd))
		if err != nil {
			//nolint:wrapcheck // WrapError adds shared encryption context.
			return contentkeys.WrapError(err)
		}

		err := manager.PrepareServe(ctx)
		if err != nil {
			//nolint:wrapcheck // WrapError adds shared encryption context.
			return contentkeys.WrapError(err)
		}

		return writeKeyring(cmd, keyring)
	case "key-remove":
		id := cmd.String(flagKeyID)
		if _, exists := keyring.Keys[id]; !exists {
			return errors.New("key ID not present in keyring")
		}

		err := manager.CanRemove(ctx, id)
		if err != nil {
			//nolint:wrapcheck // WrapError adds shared encryption context.
			return contentkeys.WrapError(err)
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
	case encryptionPrune:
		report, err = manager.PruneCorrupt(ctx, cmd.Bool(flagDryRun))
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

	//nolint:wrapcheck // WrapError adds shared encryption context.
	return contentkeys.WrapError(err)
}

func generateKeyring(cmd *cli.Command) error {
	keyring := &crypto.ContentKeyring{Active: cmd.String(flagKeyID), Keys: make(map[string][]byte)}

	err := keyring.AddKey(keyring.Active)
	if err != nil {
		//nolint:wrapcheck // WrapError adds shared encryption context.
		return contentkeys.WrapError(err)
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
		//nolint:wrapcheck // WrapError adds shared encryption context.
		return contentkeys.WrapError(writeErr)
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

// encryptionHasher hashes converted legacy edit codes with the operator's argon2 cost.
func encryptionHasher(cmd *cli.Command) *crypto.EditCodeHasher {
	return crypto.NewEditCodeHasher(argon2ParamsFromFlags(cmd))
}

func validateContentEncryption(
	ctx context.Context, db *bun.DB, keyring *crypto.ContentKeyring, hasher contentkeys.EditCodeHasher,
) error {
	manager, err := newEncryptionManager(db, keyring, hasher)
	if err != nil {
		return err
	}

	//nolint:wrapcheck // WrapError adds shared encryption context.
	return contentkeys.WrapError(manager.PrepareServe(ctx))
}

// managedSecretsDir is where Docker and Compose mount secrets. Compose bind-mounts a
// file-based secret with the host file's mode and swarm defaults to 0444, so the server
// cannot insist on an owner-only mode there; the directory itself is not world-listable.
const managedSecretsDir = "/run/secrets/"

// allowSharedKeyring reports whether a keyring at path may be accessible to group or other
// users: the operator passed --allow-shared-keyring, or the cleaned path lies inside the
// managed secrets directory. Cleaning first stops "/run/secrets/../" from escaping it.
func allowSharedKeyring(path string, allowFlag bool) bool {
	return allowFlag || strings.HasPrefix(filepath.Clean(path), managedSecretsDir)
}

// loadContentKeyring applies the server's keyring permission policy. Fail-hard: a keyring
// readable by other users stops startup unless allowSharedKeyring grants the exception, in
// which case the loader logs a warning through log, never the process-wide default logger.
func loadContentKeyring(path string, allowFlag bool, log *slog.Logger) (*crypto.ContentKeyring, error) {
	keyring, err := crypto.LoadContentKeyring(path, log, crypto.KeyringLoadOptions{
		AllowSharedPermissions: allowSharedKeyring(path, allowFlag),
	})
	if errors.Is(err, crypto.ErrKeyringPermissions) {
		return nil, fmt.Errorf("%w; if a secret manager cannot provide an owner-only file, pass --%s (%s=true)",
			err, FlagAllowSharedKeyring, EnvAllowSharedKeyring)
	}

	if err != nil {
		return nil, fmt.Errorf("load content keyring: %w", err)
	}

	return keyring, nil
}
