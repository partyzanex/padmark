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

	"github.com/partyzanex/padmark/internal/clifactory"
	"github.com/partyzanex/padmark/internal/errorcontext"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	keyrepo "github.com/partyzanex/padmark/internal/infra/storage/contentkeys"
	"github.com/partyzanex/padmark/internal/usecases/contentkeys"
)

const (
	flagKeyID     = "key-id"
	flagKeyOutput = "output"
	flagDryRun    = "dry-run"
	flagCorrupt   = "corrupt"
	flagNoteID    = "id"
)

const (
	encryptionMigrate = "migrate"
	encryptionRewrap  = "rewrap"
	encryptionConvert = "convert"
	encryptionPrune   = "prune"
	encryptionMark    = "mark-legacy"
)

func encryptionCommand() *cli.Command {
	flags := append(migrateFlags(), clifactory.StringFlag(func(flag *cli.StringFlag) {
		flag.Name = FlagContentKeyring
		flag.Sources = cli.EnvVars(EnvContentKeyring)
		flag.Usage = "Content keyring file; defaults to <sqlite db dir>/padmark-keys.json or ./padmark-keys.json " +
			"for postgres (never created by encryption commands)"
	}), allowSharedKeyringFlag())
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
		{encryptionPrune, "Permanently remove malformed raw records; preview with --corrupt --dry-run " +
			"(lists candidate IDs)"},
		{encryptionMark, "Tag one note as v0.1.6 plaintext for conversion by encryption migrate " +
			"(a 64-hex custom slug the schema migrations could not recognise)"},
	}

	commands := make([]*cli.Command, 0, len(items))
	for i := range items {
		item := &items[i]

		commands = append(commands, clifactory.Command(func(command *cli.Command) {
			command.Name = item.name
			command.Usage = item.usage
			command.Action = encryptionAction
			command.Flags = encryptionOperationFlags(item.name)
		}))
	}

	return clifactory.Command(func(command *cli.Command) {
		command.Name = "encryption"
		command.Usage = "Manage content keys, migration and rotation"
		command.Description = "Apply schema migrations first with `padmark migrate`. Stop all servers and writers " +
			"before changing encryption state or retiring keys. Keep database and keyring backups. " +
			"Key files are written only to new paths; distribute them explicitly to every replica."
		command.Flags = flags
		command.Commands = commands
	})
}

func encryptionOperationFlags(name string) []cli.Flag {
	flags := []cli.Flag{
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = flagKeyID
			flag.Usage = "Public key ID (required for key operations)"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = flagKeyOutput
			flag.Usage = "New keyring filename; existing files are never overwritten"
		}),
		clifactory.BoolFlag(func(flag *cli.BoolFlag) {
			flag.Name = flagDryRun
			flag.Usage = "Inspect migration, rewrap, conversion or pruning without writes"
		}),
	}
	if name == encryptionPrune {
		flags = append(flags, clifactory.BoolFlag(func(flag *cli.BoolFlag) {
			flag.Name = flagCorrupt
			flag.Usage = "Explicitly select malformed raw records for permanent deletion"
		}))
	}

	if name == encryptionMark {
		flags = append(flags, clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = flagNoteID
			flag.Usage = "Note ID from encryption prune --corrupt --dry-run (required)"
		}))
	}

	return flags
}

// newEncryptionManager builds the content-key manager. hasher hashes the plaintext edit codes
// of legacy (v0.1.6) notes converted by encryption migrate; serve never converts, but the
// manager always needs one.
func newEncryptionManager(
	db *bun.DB, keyring *crypto.ContentKeyring, hasher contentkeys.EditCodeHasher,
) (*contentkeys.Manager, error) {
	notes, err := keyring.ForResource("note")
	if err != nil {
		return nil, contentkeys.Wrap(err)
	}

	links, err := keyring.ForResource("link")
	if err != nil {
		return nil, contentkeys.Wrap(err)
	}

	return contentkeys.New(keyrepo.New(db), keyring, notes, links, hasher), nil
}

func validateEncryptionFlags(cmd *cli.Command) error {
	if cmd.Name == encryptionPrune && !cmd.Bool(flagCorrupt) {
		return errPruneRequiresCorrupt
	}

	if cmd.Name == encryptionMark && cmd.String(flagNoteID) == "" {
		return errMarkLegacyRequiresID
	}

	if !cmd.Bool(flagDryRun) {
		return nil
	}

	switch cmd.Name {
	case encryptionMigrate, encryptionRewrap, encryptionConvert, encryptionPrune:
		return nil
	default:
		return errUnsupportedDryRun
	}
}

func encryptionAction(ctx context.Context, cmd *cli.Command) error {
	flagErr := validateEncryptionFlags(cmd)
	if flagErr != nil {
		return fmt.Errorf("validate encryption flags: %w", flagErr)
	}

	if cmd.Name == "keygen" {
		return errorcontext.Wrap(generateKeyring(cmd), "generate keyring")
	}

	log := newLogger(DefaultLogLevel, DefaultLogFormat)

	keyring, err := loadToolKeyring(cmd.String(FlagContentKeyring), cmd.String(FlagStorage), cmd.String(FlagDSN),
		cmd.Bool(FlagAllowSharedKeyring), log)
	if err != nil {
		return contentkeys.Wrap(err)
	}

	if cmd.Name == "key-add" {
		err = keyring.AddKey(cmd.String(flagKeyID))
		if err != nil {
			return contentkeys.Wrap(err)
		}

		return errorcontext.Wrap(writeKeyring(cmd, keyring), "write keyring")
	}

	argonErr := validateArgon2Config(cmd)
	if argonErr != nil {
		return fmt.Errorf("validate argon2 config: %w", argonErr)
	}

	// Bound argon2id workspace before converted legacy edit codes are hashed.
	hasher, err := prepareArgon2(cmd)
	if err != nil {
		return fmt.Errorf("prepare argon2: %w", err)
	}

	db, err := openDB(ctx, cmd.String(FlagStorage), cmd.String(FlagDSN), log)
	if err != nil {
		return contentkeys.Wrap(err)
	}

	// All mutations commit before returning, so a close failure is only logged.
	defer closeServerDB(ctx, db, log)

	manager, err := newEncryptionManager(db, keyring, hasher)
	if err != nil {
		return contentkeys.Wrap(err)
	}

	return errorcontext.Wrap(runEncryptionOperation(ctx, cmd, db, keyring, manager, hasher),
		"run encryption operation")
}

func runEncryptionOperation(ctx context.Context, cmd *cli.Command, db *bun.DB,
	keyring *crypto.ContentKeyring, manager *contentkeys.Manager, hasher *crypto.EditCodeHasher,
) error {
	var err error

	switch cmd.Name {
	case "register":
		return contentkeys.Wrap(manager.Register(ctx, cmd.String(flagKeyID)))
	case "key-activate":
		reg, readErr := keyrepo.New(db).Registry(ctx)
		if readErr != nil {
			return contentkeys.Wrap(readErr)
		}

		if reg == nil {
			return errActivateUnregisteredKey
		}

		keyring.Active = cmd.String(flagKeyID)

		manager, err = newEncryptionManager(db, keyring, hasher)
		if err != nil {
			return contentkeys.Wrap(err)
		}

		err := manager.PrepareServe(ctx)
		if err != nil {
			return contentkeys.Wrap(err)
		}

		return errorcontext.Wrap(writeKeyring(cmd, keyring), "write keyring")
	case "key-remove":
		id := cmd.String(flagKeyID)
		if _, exists := keyring.Keys[id]; !exists {
			return errKeyIDNotInKeyring
		}

		err := manager.CanRemove(ctx, id)
		if err != nil {
			return contentkeys.Wrap(err)
		}

		delete(keyring.Keys, id)

		return errorcontext.Wrap(writeKeyring(cmd, keyring), "write keyring")
	default:
		return errorcontext.Wrap(runEncryptionData(ctx, cmd, manager), "run encryption data")
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
	case encryptionMark:
		report, err = manager.MarkLegacy(ctx, cmd.String(flagNoteID))
	default:
		return errUnknownEncryptionOperation
	}

	output := cmd.Root().Writer
	if output == nil {
		output = os.Stdout
	}

	writeErr := json.NewEncoder(output).Encode(report)
	if writeErr != nil {
		return errors.Join(err, fmt.Errorf("write encryption report: %w", writeErr))
	}

	return contentkeys.Wrap(err)
}

func generateKeyring(cmd *cli.Command) error {
	keyring := &crypto.ContentKeyring{Active: cmd.String(flagKeyID), Keys: make(map[string][]byte)}

	err := keyring.AddKey(keyring.Active)
	if err != nil {
		return contentkeys.Wrap(err)
	}

	return errorcontext.Wrap(writeKeyring(cmd, keyring), "write keyring")
}

func writeKeyring(cmd *cli.Command, keyring *crypto.ContentKeyring) error {
	path := cmd.String(flagKeyOutput)
	if path == "" {
		return errKeyringOutputRequired
	}

	writeErr := crypto.WriteContentKeyring(path, keyring)
	if writeErr != nil {
		return contentkeys.Wrap(writeErr)
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
func encryptionHasher(cmd *cli.Command) (*crypto.EditCodeHasher, error) {
	params, err := argon2ParamsFromFlags(cmd)
	if err != nil {
		return nil, fmt.Errorf("argon2 params: %w", err)
	}

	return crypto.NewEditCodeHasher(params), nil
}

// prepareArgon2 sets the process-wide argon2id memory budget from the validated flag and builds
// the edit-code hasher. Call it before any hasher runs.
func prepareArgon2(cmd *cli.Command) (*crypto.EditCodeHasher, error) {
	budget, err := uint32Flag(cmd, FlagArgon2MemoryBudget)
	if err != nil {
		return nil, fmt.Errorf("argon2 memory budget: %w", err)
	}

	crypto.SetArgon2MemoryBudget(budget)

	return encryptionHasher(cmd)
}

func validateContentEncryption(
	ctx context.Context, db *bun.DB, keyring *crypto.ContentKeyring, hasher contentkeys.EditCodeHasher, log *slog.Logger,
) error {
	manager, err := newEncryptionManager(db, keyring, hasher)
	if err != nil {
		return fmt.Errorf("new encryption manager: %w", err)
	}

	log.InfoContext(ctx, "preparing stored content; pending migrations run automatically")

	report, err := manager.MigrateIfNeeded(ctx)
	if err == nil {
		log.InfoContext(ctx, "stored content ready", "migrated", report.Changed, "wrapped", report.Wrapped,
			"native", report.Native)
	}

	return contentkeys.Wrap(err)
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
