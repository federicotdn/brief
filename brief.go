package main

import (
	"embed"
	"flag"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"gopkg.in/yaml.v3"
)

//go:embed commands
var commandSpecs embed.FS

const (
	SPEC_VERSION        = "v1"
	COMMANDS_DIR        = "commands/"
	COMMAND_SPEC_SUFFIX = ".cmd.yaml"

	LETTERS       = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	DIGITS        = "0987654321"
	PREFIX_DASH   = '-'
	PREFIX_EQUALS = '='
	PREFIX_PLUS   = '+'

	FLAG_TYPE_VALUE          = "value"
	FLAG_TYPE_VALUE_OPTIONAL = "valueOptional"
	FLAG_TYPE_TOGGLE         = "toggle"

	ENVVAR_KEY = '!'
	HELP_KEY   = '?'
	CANCEL_KEY = tcell.KeyCtrlG
	EXIT_KEY   = tcell.KeyCtrlX
)

type option struct {
	// An option can be either a flag (--foo) or an argument (also
	// known as positional argument)
	Flags    []string `yaml:"flag"`
	Argument string   `yaml:"argument"`

	Help       string `yaml:"help"`
	FlagType   string `yaml:"type"`
	Default    string `yaml:"default"`
	Repeatable bool   `yaml:"repeatable"`

	key    rune
	prefix rune
}

type optionValue struct {
	opt   *option
	value string
}

type command struct {
	Name        string     `yaml:"name"`
	Version     string     `yaml:"version"`
	Subcommands []*command `yaml:"subcommands"`
	Options     []*option  `yaml:"options"`
	Help        string     `yaml:"help"`

	key       rune
	optValues []*optionValue
}

type spec struct {
	Version string  `yaml:"specVersion"`
	Command command `yaml:"command"`
}

type application struct {
	ui                *userInterface
	sp                *spec
	enabledCommands   []*command
	environment       []string
	lastPrefix        rune
	tviewApp          *tview.Application
	minibufferActive  bool
	inputDoneCallback func(bool, string)
	cursor            int
	cursorMax         int
}

func isPrefix(r rune) bool {
	return r == PREFIX_DASH || r == PREFIX_EQUALS || r == PREFIX_PLUS
}

func (opt *option) getType() string {
	if opt.FlagType == "" {
		return FLAG_TYPE_VALUE
	}
	return opt.FlagType
}

func (opt *option) isFlag() bool {
	return opt.Argument == ""
}

func (opt *option) isArgument() bool {
	return !opt.isFlag()
}

func (opt *option) longFlag() string {
	if !opt.isFlag() {
		panic("option is not a flag")
	}
	for _, flag := range opt.Flags {
		// Return the first long flag, that is:
		//  --foobar or -foobar (yes)
		//  -f       (no)
		if len(flag) > 2 {
			return flag
		}
	}
	// No long flag was found, simply use the first one
	return opt.Flags[0]
}

func (opt *option) mainFlag() string {
	return opt.Flags[0]
}

func (cmd *command) deleteOptionValueAt(index int) {
	cmd.optValues = append(cmd.optValues[:index], cmd.optValues[index+1:]...)
}

func (cmd *command) deleteOptionValuesFor(opt *option) {
	if len(cmd.optValues) == 0 {
		return
	}
	newValues := make([]*optionValue, 0, len(cmd.optValues))
	for _, val := range cmd.optValues {
		if val.opt != opt {
			newValues = append(newValues, val)
		}
	}

	cmd.optValues = newValues
}

func (cmd *command) optionValueCount(opt *option) uint {
	var total uint
	for _, val := range cmd.optValues {
		if val.opt == opt {
			total += 1
		}
	}
	return total
}

func newApplication(sp *spec) *application {
	app := application{
		ui:              newUserInterface(),
		sp:              sp,
		enabledCommands: []*command{&sp.Command},
		tviewApp:        tview.NewApplication(),
		cursor:          math.MaxInt,
	}

	app.ui.root.SetInputCapture(app.captureRootInput)
	app.ui.minibuffer.SetInputCapture(app.captureMinibufferInput)
	app.ui.minibuffer.SetDoneFunc(app.minibufferDone)
	app.tviewApp.SetRoot(app.ui.root, true)

	app.clampCursor()
	app.updateKeys()
	app.updateViews()

	return &app
}

func (app *application) visibleCommands() []*command {
	return app.enabledCommands[len(app.enabledCommands)-1].Subcommands
}

func (app *application) updateKeys() {
	app.assignCommandKeys()
	app.assignFlagKeys()
	app.assignArgumentKeys()
}

func (app *application) assignArgumentKeys() {
	used := make(map[rune]struct{})

	for _, cmd := range app.enabledCommands {
		for _, opt := range cmd.Options {
			if !opt.isArgument() {
				continue
			}

			opt.key = 0
			opt.prefix = 0

			for _, r := range []rune(DIGITS) {
				_, found := used[r]
				if !found {
					opt.key = r
					used[r] = struct{}{}
					break
				}
			}

			if opt.key == 0 {
				panic("no key found for argument")
			}
		}
	}
}

func (app *application) assignFlagPrefixKey(opt *option, prefix, r rune, used map[string]struct{}) bool {
	candidates := []string{string([]rune{prefix, r})}

	if prefix == PREFIX_DASH {
		candidates = append(candidates, string([]rune{PREFIX_EQUALS, r}))
	}

	for _, candidate := range candidates {
		_, found := used[candidate]
		if !found {
			opt.prefix = ([]rune(candidate))[0]
			opt.key = ([]rune(candidate))[1]
			used[candidate] = struct{}{}
			return true
		}
	}

	return false
}

func (app *application) assignFlagKeys() {
	used := make(map[string]struct{})

	for _, cmd := range app.enabledCommands {
		for _, opt := range cmd.Options {
			if !opt.isFlag() {
				continue
			}

			opt.key = 0
			opt.prefix = 0

			prefix := ([]rune(opt.mainFlag()))[0]
			var found bool

			for _, r := range opt.mainFlag() + opt.longFlag() {
				if !strings.ContainsRune(LETTERS, r) && !strings.ContainsRune(DIGITS, r) {
					continue
				}

				found = app.assignFlagPrefixKey(opt, prefix, r, used)
				if found {
					break
				}
			}

			if !found {
				for _, r := range []rune(LETTERS + DIGITS) {
					found = app.assignFlagPrefixKey(opt, prefix, r, used)
					if found {
						break
					}
				}
			}

			if !found {
				panic("no key found for flag")
			}
		}
	}
}

func (app *application) assignCommandKeys() {
	used := make(map[rune]struct{})

	for _, cmd := range app.visibleCommands() {
		cmd.key = 0
		name := []rune(cmd.Name)

		for _, r := range name {
			_, contained := used[r]
			if !contained {
				used[r] = struct{}{}
				cmd.key = r
				break
			}
		}

		if cmd.key == 0 {
			// Key still hasn't been assigned, use a random one
			for _, r := range []rune(LETTERS) {
				_, contained := used[r]
				if !contained {
					used[r] = struct{}{}
					cmd.key = r
					break
				}
			}
		}

		if cmd.key == 0 {
			panic("no key found for command")
		}
	}
}

func (app *application) minibufferDone(key tcell.Key) {
	app.inputDoneCallback(key == tcell.KeyEnter, app.ui.minibuffer.GetText())

	app.ui.root.RemoveItem(app.ui.minibuffer)
	app.tviewApp.SetFocus(app.ui.root)
	app.minibufferActive = false
	app.inputDoneCallback = nil

	app.ui.root.AddItem(app.ui.messagesTextView, 1, 0, false)

	app.updateViews()
}

func (app *application) minibufferRead(prompt string, callback func(bool, string), default_ string) {
	app.ui.root.RemoveItem(app.ui.messagesTextView)
	app.ui.root.AddItem(app.ui.minibuffer, 1, 0, true)
	app.ui.minibuffer.SetLabel(prompt + " ")
	app.ui.minibuffer.SetText(default_)
	app.tviewApp.SetFocus(app.ui.minibuffer)
	app.minibufferActive = true
	app.inputDoneCallback = callback
}

func (app *application) showMessage(format string, a ...any) {
	app.ui.messagesTextView.SetText(fmt.Sprintf(" "+format, a...))
}

func (app *application) updateSubcommandsView() {
	commands := app.visibleCommands()
	cmdText := NewUIText()

	if app.lastPrefix != 0 {
		cmdText.dim()
	}

	cmdText.bold().write(" " + string(ENVVAR_KEY)).unbold()
	cmdText.write(" add environment variable\n")
	cmdText.bold().write(" " + string(HELP_KEY)).unbold()
	cmdText.write(" show help for brief\n\n")

	for _, cmd := range commands {
		cmdText.bold().write(" " + string(cmd.key) + " ").unbold()
		cmdText.write(cmd.Name)
		if cmd.Help != "" {
			cmdText.write(" - " + cmd.Help + "")
		}
		cmdText.write("\n")
	}

	app.ui.subcommandsTextView.SetText(cmdText.String())
}

func (app *application) updateCmdPreviewView() {
	previewText := NewUIText()
	regionN := 0

	for i, env := range app.environment {
		if i > 0 {
			previewText.write(" ")
		}
		previewText.write(regionInt(regionN, env))
		regionN++
	}

	for i, cmd := range app.enabledCommands {
		if i > 0 || len(app.environment) > 0 {
			previewText.write(" ")
		}

		previewText.write(regionInt(regionN, cmd.Name))
		regionN++

		for _, val := range cmd.optValues {
			opt := val.opt
			valuePreview := val.value
			if valuePreview == "" {
				valuePreview = "\"\""
			}

			if opt.isFlag() {
				if opt.FlagType == FLAG_TYPE_TOGGLE ||
					(opt.FlagType == FLAG_TYPE_VALUE_OPTIONAL && val.value == "") {
					previewText.write(" " + regionInt(regionN, opt.mainFlag()))

				} else {
					previewText.write(" " + regionInt(regionN, opt.mainFlag()+" "+valuePreview))
				}
			} else {
				previewText.write(" " + regionInt(regionN, valuePreview))

			}
			regionN++
		}
	}

	// Cursor can move one extra place to the right
	previewText.write(" " + regionInt(regionN, " "))

	app.ui.cmdPreviewTextView.SetText(previewText.String())
	app.ui.cmdPreviewTextView.Highlight(strconv.Itoa(app.cursor))
}

func (app *application) updateOptionsView() {
	optsText := NewUIText()

	for _, cmd := range app.enabledCommands {
		optsText.write(cmd.Name + ":\n")

		for _, opt := range cmd.Options {
			if !opt.isFlag() {
				continue
			}

			dim := (app.lastPrefix != 0 && opt.prefix != app.lastPrefix)
			flags := strings.Join(opt.Flags, ", ")

			if dim {
				optsText.dim()
			}

			optsText.bold().write(" " + string(opt.prefix) + string(opt.key)).unbold()
			optsText.write(" " + opt.Help + " (" + flags)

			switch opt.getType() {
			case FLAG_TYPE_VALUE:
				optsText.write(" <value>)")
			case FLAG_TYPE_VALUE_OPTIONAL:
				// The string to display is "[value]", but an extra "[" needs to be
				// added in order to prevent tview from interpreting it as a color tag.
				optsText.write(" [value[])")
			case FLAG_TYPE_TOGGLE:
				optsText.write(")")
			}

			if opt.Repeatable {
				// See comment above
				optsText.write(" [repeatable[]")
			}

			optsText.write("\n")
			optsText.undim()
		}

		for _, opt := range cmd.Options {
			if !opt.isArgument() {
				continue
			}

			if app.lastPrefix != 0 {
				optsText.dim()
			}

			optsText.bold().write("  " + string(opt.key)).unbold()
			optsText.write(" " + opt.Help + "\n")
			optsText.undim()
		}

		optsText.write("\n")
	}

	app.ui.optionsTextView.SetText(optsText.String())
}

func (app *application) updateViews() {
	app.updateSubcommandsView()
	app.updateOptionsView()
	app.updateCmdPreviewView()
}

func (app *application) handleDeletionKey(backspace bool) {
	app.lastPrefix = 0

	if (app.cursor >= app.cursorMax && !backspace) || (app.cursor <= 0 && backspace) {
		app.showMessage("nothing to delete")
		return
	}

	deleteAt := app.cursor
	cursorModifier := 0
	if backspace {
		deleteAt--
		cursorModifier = -1
	}

	i := 0

	for range app.environment {
		if i == deleteAt {
			app.environment = append(app.environment[:i], app.environment[i+1:]...)
			app.cursor += cursorModifier
			return
		}

		i++
	}

	for cmdIndex, cmd := range app.enabledCommands {
		if i == deleteAt {
			if cmdIndex != len(app.enabledCommands)-1 {
				app.showMessage("unable to delete %v command: one or more subcommands are present", cmd.Name)
				return
			} else if len(cmd.optValues) > 0 {
				app.showMessage("unable to delete %v command: options are present", cmd.Name)
				return
			} else if cmdIndex == 0 {
				app.showMessage("nothing to delete")
				return
			}

			app.enabledCommands = app.enabledCommands[:len(app.enabledCommands)-1]
			app.cursor += cursorModifier
			return
		}

		i++

		for j := range cmd.optValues {
			if i == deleteAt {
				cmd.deleteOptionValueAt(j)
				app.cursor += cursorModifier
				return
			}
			i++
		}
	}
}

func (app *application) handlePrefixKey(key rune) {
	found := false
	for _, cmd := range app.enabledCommands {
		for _, opt := range cmd.Options {
			if opt.prefix == key {
				found = true
				break
			}
		}

		if found {
			break
		}
	}

	if app.lastPrefix != key {
		if found {
			app.showMessage(string(key))
			app.lastPrefix = key
		} else {
			app.showMessage("%c is undefined", key)
			app.lastPrefix = 0
		}
	} else {
		app.showMessage("")
		app.lastPrefix = 0
	}
}

func (app *application) handleEnvvarKey() {
	if app.lastPrefix != 0 {
		app.showMessage("%c%c is undefined", app.lastPrefix, ENVVAR_KEY)
		app.lastPrefix = 0
		return
	}

	app.minibufferRead("value (VAR=VAL):", func(ok bool, val string) {
		if ok {
			parts := strings.Split(val, "=")
			if len(parts) == 1 || len(parts[0]) == 0 {
				app.showMessage("invalid environment variable format")
				return
			}
			app.environment = append(app.environment, val)
			app.cursor += 1
		}
	}, "")
}

func (app *application) handleLetterKeyNoPrefix(key rune) {
	found := false
	for _, cmd := range app.visibleCommands() {
		if cmd.key == key {
			app.enabledCommands = append(app.enabledCommands, cmd)
			app.cursor = math.MaxInt
			found = true
			break
		}
	}

	if !found {
		app.showMessage("%c is undefined", key)
	}
}

func (app *application) handleDigitKeyNoPrefix(key rune) {
	found := false

	for _, cmd := range app.enabledCommands {
		for _, opt := range cmd.Options {
			if !opt.isArgument() {
				continue
			}

			if opt.key == key {
				if cmd.optionValueCount(opt) == 0 || opt.Repeatable {
					app.promptOptionValue(cmd, opt)
				} else {
					cmd.deleteOptionValuesFor(opt)
				}
				found = true
				break
			}
		}

		if found {
			break
		}
	}

	if !found {
		app.showMessage("%c is undefined", key)
	}
}

func (app *application) promptOptionValue(cmd *command, opt *option) {
	app.minibufferRead("value:", func(ok bool, val string) {
		if ok {
			app.addOptionValue(cmd, opt, val)
		}
	}, opt.Default)
}

func (app *application) addOptionValue(cmd *command, opt *option, val string) {
	cmd.optValues = append(cmd.optValues, &optionValue{opt: opt, value: val})
	app.cursor = app.cursorMax + 1
}

func (app *application) handleLetterDigitKeyWithPrefix(key rune) {
	found := false

	for _, cmd := range app.enabledCommands {
		for _, opt := range cmd.Options {
			if !opt.isFlag() {
				continue
			}

			if opt.prefix == app.lastPrefix && opt.key == key {
				if opt.Repeatable {
					app.promptOptionValue(cmd, opt)
				} else {
					if cmd.optionValueCount(opt) == 0 {
						if opt.getType() == FLAG_TYPE_TOGGLE {
							app.addOptionValue(cmd, opt, "")
						} else {
							app.promptOptionValue(cmd, opt)
						}
					} else {
						cmd.deleteOptionValuesFor(opt)
					}
				}

				found = true
				break
			}
		}

		if found {
			break
		}
	}

	if !found {
		app.showMessage("%c%c is undefined", app.lastPrefix, key)
	}
}

func (app *application) handleLetterDigitKey(key rune) {
	if app.lastPrefix == 0 {
		if strings.ContainsRune(LETTERS, key) {
			app.handleLetterKeyNoPrefix(key)
		} else {
			app.handleDigitKeyNoPrefix(key)
		}
	} else {
		app.handleLetterDigitKeyWithPrefix(key)
	}
	app.lastPrefix = 0
}

func (app *application) handlePrintableKey(key rune) {
	if isPrefix(key) {
		app.handlePrefixKey(key)
	} else if key == ENVVAR_KEY {
		app.handleEnvvarKey()
	} else if strings.ContainsRune(LETTERS+DIGITS, key) {
		app.handleLetterDigitKey(key)
	} else if key == HELP_KEY {
		app.handleHelpKey()
	} else {
		app.showMessage("%c is undefined", key)
	}
}

func (app *application) handleCopyKey() {

}

func (app *application) handleHelpKey() {
	// Allow using the help key even if a prefix key was active,
	// for convenience.
	app.lastPrefix = 0
	app.showMessage("help")
}

func (app *application) captureRootInput(event *tcell.EventKey) *tcell.EventKey {
	if app.minibufferActive {
		return event
	}

	app.showMessage("")

	switch key := event.Key(); key {
	case CANCEL_KEY:
		app.lastPrefix = 0
	case tcell.KeyBackspace:
		fallthrough
	case tcell.KeyBackspace2:
		fallthrough
	case tcell.KeyDelete:
		app.handleDeletionKey(key != tcell.KeyDelete)
	case tcell.KeyEnter:
		fallthrough
	case tcell.KeyCtrlX:
		app.tviewApp.Stop()
	case tcell.KeyLeft:
		app.cursor--
	case tcell.KeyRight:
		app.cursor++
	case tcell.KeyRune:
		app.handlePrintableKey(event.Rune())
	}

	app.clampCursor()
	app.updateKeys()
	app.updateViews()

	return nil
}

func (app *application) clampCursor() {
	app.cursorMax = len(app.environment)
	app.cursorMax += len(app.enabledCommands)
	for _, cmd := range app.enabledCommands {
		app.cursorMax += len(cmd.optValues)
	}

	if app.cursor > app.cursorMax {
		app.cursor = app.cursorMax
	} else if app.cursor < 0 {
		app.cursor = 0
	}
}

func (app *application) captureMinibufferInput(event *tcell.EventKey) *tcell.EventKey {
	if event.Key() == CANCEL_KEY {
		return tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)
	}
	return event
}

func main() {
	flag.Parse()

	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "error: command name is required")
		flag.Usage()
		os.Exit(1)
	}

	data, err := commandSpecs.ReadFile(COMMANDS_DIR + flag.Arg(0) + COMMAND_SPEC_SUFFIX)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: command not found:", flag.Arg(0))
		os.Exit(1)
	}

	var sp spec
	err = yaml.Unmarshal(data, &sp)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: unable to unmarshal YAML data:", err)
		os.Exit(1)
	}

	if sp.Version != SPEC_VERSION {
		fmt.Fprintln(os.Stderr, "error: spec version must be", SPEC_VERSION)
		os.Exit(1)
	}

	app := newApplication(&sp)

	if err := app.tviewApp.Run(); err != nil {
		panic(err)
	}

	command := strings.TrimSpace(app.ui.cmdPreviewTextView.GetText(true))
	fmt.Println(command)

	if !clipboard.Unsupported {
		clipboard.WriteAll(command)
		fmt.Println("(copied to clipboard)")
	}
}
