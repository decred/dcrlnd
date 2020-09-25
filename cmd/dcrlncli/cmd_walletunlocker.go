package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/walletunlocker"
	"github.com/urfave/cli"
)

var createCommand = cli.Command{
	Name:     "create",
	Category: "Startup",
	Usage:    "Initialize a wallet when starting dcrlnd for the first time.",
	Description: `
	The create command is used to initialize an dcrlnd wallet from scratch for
	the very first time. This is interactive command with one required
	argument (the password), and one optional argument (the mnemonic
	passphrase).

	The first argument (the password) is required and MUST be greater than
	8 characters. This will be used to encrypt the wallet within dcrlnd. This
	MUST be remembered as it will be required to fully start up the daemon.

	The second argument is an optional 24-word mnemonic derived from BIP
	39. If provided, then the internal wallet will use the seed derived
	from this mnemonic to generate all keys.

	This command returns a 24-word seed in the scenario that NO mnemonic
	was provided by the user. This should be written down as it can be used
	to potentially recover all on-chain funds, and most off-chain funds as
	well.

	Finally, it's also possible to use this command and a set of static
	channel backups to trigger a recover attempt for the provided Static
	Channel Backups. Only one of the three parameters will be accepted. See
	the 'restorechanbackup' command for further details w.r.t the format
	accepted.
	`,
	Flags: []cli.Flag{
		cli.StringFlag{
			Name: "single_backup",
			Usage: "A hex encoded single channel backup obtained " +
				"from exportchanbackup",
		},
		cli.StringFlag{
			Name: "multi_backup",
			Usage: "A hex encoded multi-channel backup obtained " +
				"from exportchanbackup",
		},
		cli.StringFlag{
			Name:  "multi_file",
			Usage: "The path to a multi-channel back up file",
		},
	},
	Action: actionDecorator(create),
}

// monowidthColumns takes a set of words, and the number of desired columns,
// and returns a new set of words that have had white space appended to the
// word in order to create a mono-width column.
func monowidthColumns(words []string, ncols int) []string {
	// Determine max size of words in each column.
	colWidths := make([]int, ncols)
	for i, word := range words {
		col := i % ncols
		curWidth := colWidths[col]
		if len(word) > curWidth {
			colWidths[col] = len(word)
		}
	}

	// Append whitespace to each word to make columns mono-width.
	finalWords := make([]string, len(words))
	for i, word := range words {
		col := i % ncols
		width := colWidths[col]

		diff := width - len(word)
		finalWords[i] = word + strings.Repeat(" ", diff)
	}

	return finalWords
}

func create(ctx *cli.Context) error {
	ctxb := context.Background()
	client, cleanUp := getWalletUnlockerClient(ctx)
	defer cleanUp()

	var (
		chanBackups *lnrpc.ChanBackupSnapshot

		// We use var restoreSCB to track if we will be including an SCB
		// recovery in the init wallet request.
		restoreSCB = false
	)

	backups, err := parseChanBackups(ctx)

	// We'll check to see if the user provided any static channel backups (SCB),
	// if so, we will warn the user that SCB recovery closes all open channels
	// and ask them to confirm their intention.
	// If the user agrees, we'll add the SCB recovery onto the final init wallet
	// request.
	switch {
	// parseChanBackups returns an errMissingBackup error (which we ignore) if
	// the user did not request a SCB recovery.
	case err == errMissingChanBackup:

	// Passed an invalid channel backup file.
	case err != nil:
		return fmt.Errorf("unable to parse chan backups: %v", err)

	// We have an SCB recovery option with a valid backup file.
	default:

	warningLoop:
		for {

			fmt.Println()
			fmt.Printf("WARNING: You are attempting to restore from a " +
				"static channel backup (SCB) file.\nThis action will CLOSE " +
				"all currently open channels, and you will pay on-chain fees." +
				"\n\nAre you sure you want to recover funds from a" +
				" static channel backup? (Enter y/n): ")

			reader := bufio.NewReader(os.Stdin)
			answer, err := reader.ReadString('\n')
			if err != nil {
				return err
			}

			answer = strings.TrimSpace(answer)
			answer = strings.ToLower(answer)

			switch answer {
			case "y":
				restoreSCB = true
				break warningLoop
			case "n":
				fmt.Println("Aborting SCB recovery")
				return nil
			}
		}
	}

	// Proceed with SCB recovery.
	if restoreSCB {
		fmt.Println("Static Channel Backup (SCB) recovery selected!")
		if backups != nil {
			switch {
			case backups.GetChanBackups() != nil:
				singleBackup := backups.GetChanBackups()
				chanBackups = &lnrpc.ChanBackupSnapshot{
					SingleChanBackups: singleBackup,
				}

			case backups.GetMultiChanBackup() != nil:
				multiBackup := backups.GetMultiChanBackup()
				chanBackups = &lnrpc.ChanBackupSnapshot{
					MultiChanBackup: &lnrpc.MultiChanBackup{
						MultiChanBackup: multiBackup,
					},
				}
			}
		}

	}

	walletPassword, err := capturePassword(
		"Input wallet password: ", false, walletunlocker.ValidatePassword,
	)
	if err != nil {
		return err
	}

	// Next, we'll see if the user has 24-word mnemonic they want to use to
	// derive a seed within the wallet.
	var (
		hasMnemonic bool
	)

mnemonicCheck:
	for {
		fmt.Println()
		fmt.Printf("Do you have an existing cipher seed " +
			"mnemonic you want to use? (Enter y/n): ")

		reader := bufio.NewReader(os.Stdin)
		answer, err := reader.ReadString('\n')
		if err != nil {
			return err
		}

		fmt.Println()

		answer = strings.TrimSpace(answer)
		answer = strings.ToLower(answer)

		switch answer {
		case "y":
			hasMnemonic = true
			break mnemonicCheck
		case "n":
			hasMnemonic = false
			break mnemonicCheck
		}
	}

	// If the user *does* have an existing seed they want to use, then
	// we'll read that in directly from the terminal.
	var (
		cipherSeedMnemonic []string
		aezeedPass         []byte
		recoveryWindow     int32
	)
	if hasMnemonic {
		// We'll now prompt the user to enter in their 24-word
		// mnemonic.
		fmt.Printf("Input your 24-word mnemonic separated by spaces: ")
		reader := bufio.NewReader(os.Stdin)
		mnemonic, err := reader.ReadString('\n')
		if err != nil {
			return err
		}

		// We'll trim off extra spaces, and ensure the mnemonic is all
		// lower case, then populate our request.
		mnemonic = strings.TrimSpace(mnemonic)
		mnemonic = strings.ToLower(mnemonic)

		cipherSeedMnemonic = strings.Split(mnemonic, " ")

		fmt.Println()

		if len(cipherSeedMnemonic) != 24 {
			return fmt.Errorf("wrong cipher seed mnemonic "+
				"length: got %v words, expecting %v words",
				len(cipherSeedMnemonic), 24)
		}

		// Additionally, the user may have a passphrase, that will also
		// need to be provided so the daemon can properly decipher the
		// cipher seed.
		aezeedPass, err = readPassword("Input your cipher seed " +
			"passphrase (press enter if your seed doesn't have a " +
			"passphrase): ")
		if err != nil {
			return err
		}

		for {
			fmt.Println()
			fmt.Printf("Input an optional address look-ahead "+
				"used to scan for used keys (default %d): ",
				defaultRecoveryWindow)

			reader := bufio.NewReader(os.Stdin)
			answer, err := reader.ReadString('\n')
			if err != nil {
				return err
			}

			fmt.Println()

			answer = strings.TrimSpace(answer)

			if len(answer) == 0 {
				recoveryWindow = defaultRecoveryWindow
				break
			}

			lookAhead, err := strconv.Atoi(answer)
			if err != nil {
				fmt.Printf("Unable to parse recovery "+
					"window: %v", err)
				continue
			}

			recoveryWindow = int32(lookAhead)
			break
		}
	} else {
		// Otherwise, if the user doesn't have a mnemonic that they
		// want to use, we'll generate a fresh one with the GenSeed
		// command.
		fmt.Println("Your cipher seed can optionally be encrypted.")

		instruction := "Input your passphrase if you wish to encrypt it " +
			"(or press enter to proceed without a cipher seed " +
			"passphrase): "
		aezeedPass, err = capturePassword(
			instruction, true, func(_ []byte) error { return nil },
		)
		if err != nil {
			return err
		}

		fmt.Println()
		fmt.Println("Generating fresh cipher seed...")
		fmt.Println()

		genSeedReq := &lnrpc.GenSeedRequest{
			AezeedPassphrase: aezeedPass,
		}
		seedResp, err := client.GenSeed(ctxb, genSeedReq)
		if err != nil {
			return fmt.Errorf("unable to generate seed: %v", err)
		}

		cipherSeedMnemonic = seedResp.CipherSeedMnemonic
	}

	// Before we initialize the wallet, we'll display the cipher seed to
	// the user so they can write it down.
	mnemonicWords := cipherSeedMnemonic

	fmt.Println("!!!YOU MUST WRITE DOWN THIS SEED TO BE ABLE TO " +
		"RESTORE THE WALLET!!!")

	fmt.Println("---------------BEGIN LND CIPHER SEED---------------")

	numCols := 4
	colWords := monowidthColumns(mnemonicWords, numCols)
	for i := 0; i < len(colWords); i += numCols {
		fmt.Printf("%2d. %3s  %2d. %3s  %2d. %3s  %2d. %3s\n",
			i+1, colWords[i], i+2, colWords[i+1], i+3,
			colWords[i+2], i+4, colWords[i+3])
	}

	fmt.Println("---------------END LND CIPHER SEED-----------------")

	fmt.Println("\n!!!YOU MUST WRITE DOWN THIS SEED TO BE ABLE TO " +
		"RESTORE THE WALLET!!!")

	// With either the user's prior cipher seed, or a newly generated one,
	// we'll go ahead and initialize the wallet.
	req := &lnrpc.InitWalletRequest{
		WalletPassword:     walletPassword,
		CipherSeedMnemonic: cipherSeedMnemonic,
		AezeedPassphrase:   aezeedPass,
		RecoveryWindow:     recoveryWindow,
		ChannelBackups:     chanBackups,
	}
	if _, err := client.InitWallet(ctxb, req); err != nil {
		return err
	}

	fmt.Println("\ndcrlnd successfully initialized!")

	return nil
}

// capturePassword returns a password value that has been entered twice by the
// user, to ensure that the user knows what password they have entered. The user
// will be prompted to retry until the passwords match. If the optional param is
// true, the function may return an empty byte array if the user opts against
// using a password.
func capturePassword(instruction string, optional bool,
	validate func([]byte) error) ([]byte, error) {

	for {
		password, err := readPassword(instruction)
		if err != nil {
			return nil, err
		}

		// Do not require users to repeat password if
		// it is optional and they are not using one.
		if len(password) == 0 && optional {
			return nil, nil
		}

		// If the password provided is not valid, restart
		// password capture process from the beginning.
		if err := validate(password); err != nil {
			fmt.Println(err.Error())
			fmt.Println()
			continue
		}

		passwordConfirmed, err := readPassword("Confirm password: ")
		if err != nil {
			return nil, err
		}

		if bytes.Equal(password, passwordConfirmed) {
			return password, nil
		}

		fmt.Println("Passwords don't match, please try again")
		fmt.Println()
	}
}

var unlockCommand = cli.Command{
	Name:     "unlock",
	Category: "Startup",
	Usage:    "Unlock an encrypted wallet at startup.",
	Description: `
	The unlock command is used to decrypt dcrlnd's wallet state in order to
	start up. This command MUST be run after booting up dcrlnd before it's
	able to carry out its duties. An exception is if a user is running with
	'--noseedbackup', then a default passphrase will be used.
	`,
	Flags: []cli.Flag{
		cli.IntFlag{
			Name: "recovery_window",
			Usage: "Address lookahead to resume recovery rescan, " +
				"value should be non-zero --  To recover all " +
				"funds, this should be greater than the " +
				"maximum number of consecutive, unused " +
				"addresses ever generated by the wallet.",
		},
		cli.BoolFlag{
			Name: "stdin",
			Usage: "read password from standard input instead of " +
				"prompting for it. THIS IS CONSIDERED TO " +
				"BE DANGEROUS if the password is located in " +
				"a file that can be read by another user. " +
				"This flag should only be used in " +
				"combination with some sort of password " +
				"manager or secrets vault.",
		},
	},
	Action: actionDecorator(unlock),
}

func unlock(ctx *cli.Context) error {
	ctxb := context.Background()
	client, cleanUp := getWalletUnlockerClient(ctx)
	defer cleanUp()

	var (
		pw  []byte
		err error
	)
	switch {
	// Read the password from standard in as if it were a file. This should
	// only be used if the password is piped into lncli from some sort of
	// password manager. If the user types the password instead, it will be
	// echoed in the console.
	case ctx.IsSet("stdin"):
		reader := bufio.NewReader(os.Stdin)
		pw, err = reader.ReadBytes('\n')

		// Remove carriage return and newline characters.
		pw = bytes.Trim(pw, "\r\n")

	// Read the password from a terminal by default. This requires the
	// terminal to be a real tty and will fail if a string is piped into
	// lncli.
	default:
		pw, err = readPassword("Input wallet password: ")
	}
	if err != nil {
		return err
	}

	args := ctx.Args()

	// Parse the optional recovery window if it is specified. By default,
	// the recovery window will be 0, indicating no lookahead should be
	// used.
	var recoveryWindow int32
	switch {
	case ctx.IsSet("recovery_window"):
		recoveryWindow = int32(ctx.Int64("recovery_window"))
	case args.Present():
		window, err := strconv.ParseInt(args.First(), 10, 64)
		if err != nil {
			return err
		}
		recoveryWindow = int32(window)
	}

	req := &lnrpc.UnlockWalletRequest{
		WalletPassword: pw,
		RecoveryWindow: recoveryWindow,
	}
	_, err = client.UnlockWallet(ctxb, req)
	if err != nil {
		return err
	}

	fmt.Println("\ndcrlnd successfully unlocked!")

	// TODO(roasbeef): add ability to accept hex single and multi backups

	return nil
}

var changePasswordCommand = cli.Command{
	Name:     "changepassword",
	Category: "Startup",
	Usage:    "Change an encrypted wallet's password at startup.",
	Description: `
	The changepassword command is used to Change dcrlnd's encrypted wallet's
	password. It will automatically unlock the daemon if the password change
	is successful.

	If one did not specify a password for their wallet (running dcrlnd with
	'--noseedbackup'), one must restart their daemon without
	'--noseedbackup' and use this command. The "current password" field
	should be left empty.
	`,
	Action: actionDecorator(changePassword),
}

func changePassword(ctx *cli.Context) error {
	ctxb := context.Background()
	client, cleanUp := getWalletUnlockerClient(ctx)
	defer cleanUp()

	currentPw, err := readPassword("Input current wallet password: ")
	if err != nil {
		return err
	}

	newPw, err := readPassword("Input new wallet password: ")
	if err != nil {
		return err
	}

	confirmPw, err := readPassword("Confirm new wallet password: ")
	if err != nil {
		return err
	}

	if !bytes.Equal(newPw, confirmPw) {
		return fmt.Errorf("passwords don't match")
	}

	req := &lnrpc.ChangePasswordRequest{
		CurrentPassword: currentPw,
		NewPassword:     newPw,
	}

	_, err = client.ChangePassword(ctxb, req)
	if err != nil {
		return err
	}

	return nil
}
