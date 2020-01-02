package cobra

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/spf13/pflag"
)

// Hidden command to request completion results from the program.
// Used by the shell completion script.
const compRequestCmd = "__complete"

// Global map of flag completion functions.
var flagCompletionFunctions = map[*pflag.Flag]func(cmd *Command, args []string, toComplete string) ([]string, BashCompDirective){}

// BashCompDirective is a bit map representing the different behaviors the shell
// can be instructed to have once completions have been provided.
type BashCompDirective int

const (
	// BashCompDirectiveError indicates an error occurred and completions should be ignored.
	BashCompDirectiveError BashCompDirective = 1 << iota

	// BashCompDirectiveNoSpace indicates that the shell should not add a space
	// after the completion even if there is a single completion provided.
	BashCompDirectiveNoSpace

	// BashCompDirectiveNoFileComp indicates that the shell should not provide
	// file completion even when no completion is provided.
	// This currently does not work for zsh or bash < 4
	BashCompDirectiveNoFileComp

	// BashCompDirectiveDefault indicates to let the shell perform its default
	// behavior after completions have been provided.
	BashCompDirectiveDefault BashCompDirective = 0
)

// RegisterFlagCompletionFunc should be called to register a function to provide completion for a flag.
func (c *Command) RegisterFlagCompletionFunc(flagName string, f func(cmd *Command, args []string, toComplete string) ([]string, BashCompDirective)) {
	flag := c.Flag(flagName)
	if flag == nil {
		log.Fatal(fmt.Sprintf("RegisterFlagCompletionFunc: flag '%s' does not exist", flagName))
	}
	if _, exists := flagCompletionFunctions[flag]; exists {
		log.Fatal(fmt.Sprintf("RegisterFlagCompletionFunc: flag '%s' already registered", flagName))
	}
	flagCompletionFunctions[flag] = f
}

// Returns a string listing the different directive enabled in the specified parameter
func (d BashCompDirective) string() string {
	var directives []string
	if d&BashCompDirectiveError != 0 {
		directives = append(directives, "BashCompDirectiveError")
	}
	if d&BashCompDirectiveNoSpace != 0 {
		directives = append(directives, "BashCompDirectiveNoSpace")
	}
	if d&BashCompDirectiveNoFileComp != 0 {
		directives = append(directives, "BashCompDirectiveNoFileComp")
	}
	if len(directives) == 0 {
		directives = append(directives, "BashCompDirectiveDefault")
	}

	if d > BashCompDirectiveError+BashCompDirectiveNoSpace+BashCompDirectiveNoFileComp {
		return fmt.Sprintf("ERROR: unexpected BashCompDirective value: %d", d)
	}
	return strings.Join(directives, ", ")
}

// Adds a special hidden command that can be used to request custom completions.
func (c *Command) initCompleteCmd() {
	completeCmd := &Command{
		Use:                   fmt.Sprintf("%s [command-line]", compRequestCmd),
		DisableFlagsInUseLine: true,
		Hidden:                true,
		DisableFlagParsing:    true,
		Args:                  MinimumNArgs(1),
		Short:                 "Request shell completion choices for the specified command-line",
		Long: fmt.Sprintf("%s is a special command that is used by the shell completion logic\n%s",
			compRequestCmd, "to request completion choices for the specified command-line."),
		Run: func(cmd *Command, args []string) {
			CompDebugln(fmt.Sprintf("%s was called with args %v", cmd.Name(), args), false)

			flag, trimmedArgs, toComplete, err := checkIfFlagCompletion(cmd.Root(), args[:len(args)-1], args[len(args)-1])
			if err != nil {
				// Error while attempting to parse flags
				CompErrorln(err.Error())
				return
			}

			// Find the real command for which completion must be performed
			finalCmd, finalArgs, err := cmd.Root().Find(trimmedArgs)
			if err != nil {
				// Unable to find the real command. E.g., <program> someInvalidCmd <TAB>
				return
			}

			CompDebugln(fmt.Sprintf("Found final command '%s', with finalArgs %v", finalCmd.Name(), finalArgs), false)

			// Parse the flags and extract the arguments to prepare for calling the completion function
			if err = finalCmd.ParseFlags(finalArgs); err != nil {
				CompErrorln(fmt.Sprintf("Error while parsing flags from args %v: %s", finalArgs, err.Error()))
				return
			}
			argsWoFlags := finalCmd.Flags().Args()
			CompDebugln(fmt.Sprintf("Args without flags are '%v' with length %d", argsWoFlags, len(argsWoFlags)), false)

			var completionFn func(cmd *Command, args []string, toComplete string) ([]string, BashCompDirective)
			var nameStr string
			if flag != nil {
				completionFn = flagCompletionFunctions[flag]
				nameStr = flag.Name
			} else {
				completionFn = finalCmd.ValidArgsFunction
				nameStr = finalCmd.CommandPath()
			}
			if completionFn == nil {
				CompErrorln(fmt.Sprintf("Go custom completion not supported/needed for flag or command: %s", nameStr))
				return
			}

			CompDebugln(fmt.Sprintf("Calling completion method for subcommand '%s' with args '%v' and toComplete '%s'", finalCmd.Name(), argsWoFlags, toComplete), false)
			completions, directive := completionFn(finalCmd, argsWoFlags, toComplete)
			for _, comp := range completions {
				// Print each possible completion to stdout for the completion script to consume.
				fmt.Fprintln(finalCmd.OutOrStdout(), comp)
			}

			// As the last printout, print the completion directive for the completion script to parse.
			// The directive integer must be that last character following a single colon (:).
			// The completion script expects :<directive>
			fmt.Fprintln(finalCmd.OutOrStdout(), fmt.Sprintf(":%d", directive))

			// Print some helpful info to stderr for the user to understand.
			// Output from stderr should be ignored by the completion script.
			fmt.Fprintf(finalCmd.ErrOrStderr(), "Completion ended with directive: %s\n", directive.string())
		},
	}
	c.AddCommand(completeCmd)
}

func isFlag(arg string) bool {
	return len(arg) > 0 && arg[0] == '-'
}

func checkIfFlagCompletion(rootCmd *Command, args []string, lastArg string) (*pflag.Flag, []string, string, error) {
	var flagName string
	trimmedArgs := args
	flagWithEqual := false
	if isFlag(lastArg) {
		if index := strings.Index(lastArg, "="); index >= 0 {
			flagName = strings.TrimLeft(lastArg[:index], "-")
			lastArg = lastArg[index+1:]
			flagWithEqual = true
		} else {
			return nil, nil, "", errors.New("Unexpected completion request for flag")
		}
	}

	if len(flagName) == 0 {
		if len(args) > 0 {
			prevArg := args[len(args)-1]
			if isFlag(prevArg) {
				// If the flag contains an = it means it has already been fully processed
				if index := strings.Index(prevArg, "="); index < 0 {
					flagName = strings.TrimLeft(prevArg, "-")

					// Remove the uncompleted flag or else Cobra could complain about
					// an invalid value for that flag e.g., helm status --output j<TAB>
					trimmedArgs = args[:len(args)-1]
				}
			}
		}
	}

	if len(flagName) == 0 {
		// Not doing flag completion
		return nil, trimmedArgs, lastArg, nil
	}

	// Find the real command for which completion must be performed
	finalCmd, _, err := rootCmd.Find(trimmedArgs)
	if err != nil {
		// Unable to find the real command. E.g., helm invalidCmd <TAB>
		return nil, nil, "", errors.New("Unable to find final command for completion")
	}

	CompDebugln(fmt.Sprintf("checkIfFlagCompletion: found final command '%s'", finalCmd.Name()), false)

	flag := findFlag(finalCmd, flagName)
	if flag == nil {
		// Flag not supported by this command, nothing to complete
		err = fmt.Errorf("Subcommand '%s' does not support flag '%s'", finalCmd.Name(), flagName)
		return nil, nil, "", err
	}

	if !flagWithEqual {
		if len(flag.NoOptDefVal) != 0 {
			// We had assumed dealing with a two-word flag but the flag is a boolean flag.
			// In that case, there is no value following it, so we are not really doing flag completion.
			// Reset everything to do argument completion.
			trimmedArgs = args
			flag = nil
		}
	}

	return flag, trimmedArgs, lastArg, nil
}

func findFlag(cmd *Command, name string) *pflag.Flag {
	flagSet := cmd.Flags()
	if len(name) == 1 {
		// First convert the short flag into a long flag
		// as the cmd.Flag() search only accepts long flags
		if short := flagSet.ShorthandLookup(name); short != nil {
			CompDebugln(fmt.Sprintf("checkIfFlagCompletion: found flag '%s' which we will change to '%s'", name, short.Name), false)
			name = short.Name
		} else {
			set := cmd.InheritedFlags()
			if short = set.ShorthandLookup(name); short != nil {
				CompDebugln(fmt.Sprintf("checkIfFlagCompletion: found inherited flag '%s' which we will change to '%s'", name, short.Name), false)
				name = short.Name
			} else {
				return nil
			}
		}
	}
	return cmd.Flag(name)
}

// CompDebug prints the specified string to the same file as where the
// completion script prints its logs.
// Note that completion printouts should never be on stdout as they would
// be wrongly interpreted as actual completion choices by the completion script.
func CompDebug(msg string, printToStdErr bool) {
	msg = fmt.Sprintf("[Debug] %s", msg)

	// Such logs are only printed when the user has set the environment
	// variable BASH_COMP_DEBUG_FILE to the path of some file to be used.
	if path := os.Getenv("BASH_COMP_DEBUG_FILE"); path != "" {
		f, err := os.OpenFile(path,
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err == nil {
			defer f.Close()
			f.WriteString(msg)
		}
	}

	if printToStdErr {
		// Must print to stderr for this not to be read by the completion script.
		fmt.Fprintf(os.Stderr, msg)
	}
}

// CompDebugln prints the specified string with a newline at the end
// to the same file as where the completion script prints its logs.
// Such logs are only printed when the user has set the environment
// variable BASH_COMP_DEBUG_FILE to the path of some file to be used.
func CompDebugln(msg string, printToStdErr bool) {
	CompDebug(fmt.Sprintf("%s\n", msg), printToStdErr)
}

// CompError prints the specified completion message to stderr.
func CompError(msg string) {
	msg = fmt.Sprintf("[Error] %s", msg)
	CompDebug(msg, true)
}

// CompErrorln prints the specified completion message to stderr with a newline at the end.
func CompErrorln(msg string) {
	CompError(fmt.Sprintf("%s\n", msg))
}
