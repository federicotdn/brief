package main

import (
	"embed"
	"flag"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
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
	SPEC_VERSION        = "1.0.0"
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

	QUOTING_SINGLE = "single"
	QUOTING_DOUBLE = "double"

	ENVVAR_KEY = '!'
	HELP_KEY   = '?'
	CANCEL_KEY = tcell.KeyCtrlG

	MAX_COMPLETIONS = 40

	KEY_COLOR    = "deeppink"
	ARG_ON_COLOR = "orange"
)

type option struct {
	// An option can be either a flag (--foo) or an argument (also
	// known as positional argument)
	Flags    []string `yaml:"flag"`
	Argument string   `yaml:"argument"`

	// Syntactic properties of the option itself
	FlagType   string `yaml:"type"`
	Repeatable bool   `yaml:"repeatable"`
	Separator  string `yaml:"separator"`
	Quoting    string `yaml:"quoting"`

	// Properties to make querying for a value easier/quicker
	Default     string           `yaml:"default"`
	Placeholder string           `yaml:"placeholder"`
	Completion  optionCompletion `yaml:"completion"`
	Metavar     string           `yaml:"metavar"`

	// A description of the option
	Help string `yaml:"help"`

	// Runtime variables
	key    rune
	prefix rune
}

type optionCompletion struct {
	Values []string `yaml:"values"`
	Cmd    []string `yaml:"command"`
}

type optionValue struct {
	// The option this struct represents a value for
	opt *option
	// The value itself
	value string
	// If this optionValue corresponds to a flag option,
	// and the flag option is is a template (e.g.
	// --validate-<thing>), then this variable contains
	// the completed version of the flag. Otherwise, it
	// contains the empty string.
	flag string
}

type subcommand struct {
	Name        string        `yaml:"name"`
	Subcommands []*subcommand `yaml:"subcommands"`
	Options     []*option     `yaml:"options"`
	Help        string        `yaml:"help"`

	key       rune
	optValues []*optionValue
}

type command struct {
	Name        string        `yaml:"name"`
	Version     string        `yaml:"version"`
	Subcommands []*subcommand `yaml:"subcommands"`
	Options     []*option     `yaml:"options"`
	Help        string        `yaml:"help"`
}

type spec struct {
	Version string  `yaml:"specVersion"`
	Command command `yaml:"command"`
}

type application struct {
	ui                    *userInterface
	sp                    *spec
	enabledCommands       []*subcommand
	environment           []string
	lastPrefix            rune
	tviewApp              *tview.Application
	minibufferActive      bool
	helpActive            bool
	minibufferCompletions []string
	inputDoneCallback     func(bool, string)
	cursor                int
	cursorMax             int
	onCloseCallback       func()
	initialized           bool
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

func (opt *option) isTemplate() bool {
	match, err := regexp.MatchString("<.+?>", opt.longFlag())
	if err != nil {
		panic(err)
	}

	return match
}

func (opt *option) longFlag() string {
	if !opt.isFlag() {
		panic("option is not a flag")
	}

	flags := make([]string, len(opt.Flags))
	copy(flags, opt.Flags)
	sort.Strings(flags)

	for _, flag := range flags {
		// Return the first long flag in alphabetical order:
		//  --foobar or -foobar (yes)
		//  -f                  (no)
		if len(flag) > 2 {
			return flag
		}
	}
	// No long flag was found, return the empty string
	return ""
}

func (opt *option) mainFlag() string {
	if !opt.isFlag() {
		panic("option is not a flag")
	}

	return opt.Flags[0]
}

func (cmd *subcommand) deleteOptionValueAt(index int) {
	cmd.optValues = append(cmd.optValues[:index], cmd.optValues[index+1:]...)
}

func (cmd *subcommand) deleteOptionValuesFor(opt *option) {
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

func (cmd *subcommand) isOptionEnabled(opt *option) bool {
	for _, val := range cmd.optValues {
		if val.opt == opt {
			return true
		}
	}
	return false
}

func newApplication(sp *spec) *application {
	root := subcommand{
		Name:        sp.Command.Name,
		Subcommands: sp.Command.Subcommands,
		Options:     sp.Command.Options,
		Help:        sp.Command.Help,
	}

	app := application{
		ui:              newUserInterface(len(root.Subcommands) > 0),
		sp:              sp,
		enabledCommands: []*subcommand{&root},
		tviewApp:        tview.NewApplication(),
		cursor:          math.MaxInt,
	}

	app.ui.root.SetInputCapture(app.captureRootInput)
	app.ui.minibuffer.SetInputCapture(app.captureMinibufferInput)
	app.ui.minibuffer.SetDoneFunc(app.minibufferDone)
	app.ui.minibuffer.SetAutocompleteFunc(app.minibufferAutocomplete)
	app.ui.minibuffer.SetAutocompletedFunc(app.minibufferAutocompletedFunc)
	app.tviewApp.SetRoot(app.ui.root, true)

	return &app
}

func (app *application) visibleCommands() []*subcommand {
	return app.enabledCommands[len(app.enabledCommands)-1].Subcommands
}

func (app *application) updateKeys() {
	app.assignCommandKeys()
	app.assignFlagKeys()
	app.assignArgumentKeys()
}

func (app *application) getHeight() int {
	_, _, _, height := app.ui.root.GetRect()
	return height
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
	app.ui.root.RemoveItem(app.ui.minibuffer)
	app.tviewApp.SetFocus(app.ui.root)
	app.minibufferActive = false
	app.minibufferCompletions = nil

	app.ui.root.AddItem(app.ui.messagesTextView, 1, 0, false)

	// Invoke the callback after clearing all state, as the callback
	// may call minibufferRead again. This happens in cases where
	// two consecutive values need to be read.
	app.inputDoneCallback(key == tcell.KeyEnter, app.ui.minibuffer.GetText())
	app.updateViews()
}

func (app *application) minibufferRead(prompt string, callback func(bool, string), default_ string, placeholder string, completions []string) {
	app.ui.root.RemoveItem(app.ui.messagesTextView)
	app.ui.root.AddItem(app.ui.minibuffer, 1, 0, true)
	app.ui.minibuffer.SetLabel(" " + prompt + " ")
	app.ui.minibuffer.SetText(default_)
	app.ui.minibuffer.SetPlaceholder(placeholder)
	app.tviewApp.SetFocus(app.ui.minibuffer)
	app.minibufferActive = true
	app.minibufferCompletions = completions
	app.inputDoneCallback = callback

	if app.minibufferCompletions != nil {
		app.ui.minibuffer.Autocomplete()
	}
}

func (app *application) minibufferAutocomplete(currentText string) []string {
	if app.minibufferCompletions == nil || len(app.minibufferCompletions) == 0 {
		return nil
	}

	completions := []string{}
	count := 0
	for _, candidate := range app.minibufferCompletions {
		match := strings.HasPrefix(strings.ToLower(candidate), strings.ToLower(currentText))

		if match {
			count++
			height := app.getHeight()
			// By substracting 3 we leave some space on top and below the list to
			// make the presentation nicer.
			if len(completions) < height-3 {
				completions = append(completions, candidate)
			}
		}
	}

	if count > len(completions) {
		completions = append(completions, fmt.Sprintf(" [%v results omitted]", count-len(completions)))
	}

	return completions
}

func (app *application) minibufferAutocompletedFunc(text string, index, source int) bool {
	if source != tview.AutocompletedNavigate {
		app.ui.minibuffer.SetText(text)
	}
	return source == tview.AutocompletedEnter || source == tview.AutocompletedClick
}

func (app *application) showMessage(format string, a ...any) {
	app.ui.messagesTextView.SetText(fmt.Sprintf(" "+format, a...))
}

func (app *application) updateSubcommandsView() {
	commands := app.visibleCommands()
	if len(commands) == 0 {
		app.ui.subcommandsTextView.SetText("")
		return
	}

	cmdText := NewUIText(false, 0)

	if app.lastPrefix != 0 {
		cmdText.dim()
	}

	for _, cmd := range commands {
		cmdText.color(KEY_COLOR).bold().write(" " + string(cmd.key) + "  ").reset()
		cmdText.write(cmd.Name)
		if cmd.Help != "" {
			cmdText.dim().write(" " + cmd.Help).undim()
		}
		cmdText.nl()
	}

	app.ui.subcommandsTextView.SetText(cmdText.page(0))
}

func (app *application) currentCommand() string {
	return strings.TrimSpace(app.ui.cmdPreviewTextView.GetText(true))
}

func (app *application) updateCmdPreviewView() {
	previewText := NewUIText(false, 0)
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

			if opt.Quoting == QUOTING_SINGLE {
				valuePreview = "'" + valuePreview + "'"
			} else if opt.Quoting == QUOTING_DOUBLE {
				valuePreview = "\"" + valuePreview + "\""
			} else if valuePreview == "" {
				// Quote empty values using double quotes, by default
				valuePreview = "\"\""
			}

			if opt.isFlag() {
				flagText := opt.mainFlag()
				if val.flag != "" {
					flagText = val.flag
				}

				if opt.FlagType == FLAG_TYPE_TOGGLE ||
					(opt.FlagType == FLAG_TYPE_VALUE_OPTIONAL && val.value == "") {
					previewText.write(" " + regionInt(regionN, flagText))
				} else {
					sep := " "
					if opt.Separator != "" {
						sep = opt.Separator
					}
					previewText.write(" " + regionInt(regionN, flagText+sep+valuePreview))
				}
			} else {
				previewText.write(" " + regionInt(regionN, valuePreview))

			}
			regionN++
		}
	}

	// Cursor can move one extra place to the right
	previewText.write(" " + regionInt(regionN, " "))

	app.ui.cmdPreviewTextView.SetText(previewText.page(0))
	app.ui.cmdPreviewTextView.Highlight(strconv.Itoa(app.cursor))
}

func (app *application) updateOptionsTitle() {
	front, _ := app.ui.optionsPages.GetFrontPage()
	i, _ := strconv.Atoi(front)
	count := app.ui.optionsPages.GetPageCount()
	app.ui.optionsFlex.SetTitle(fmt.Sprintf("Options (page %v of %v)", i+1, count))
}

func (app *application) updateOptionsView() {
	_, _, _, pagesHeight := app.ui.optionsPages.GetRect()
	front, _ := app.ui.optionsPages.GetFrontPage()
	optsText := NewUIText(true, pagesHeight)

	for i, cmd := range app.enabledCommands {
		if i > 0 && len(cmd.Options) == 0 {
			continue
		}

		optsText.bold().write(cmd.Name + ":").nl().unbold()

		if i == 0 {
			optsText.color(KEY_COLOR).bold().write("  " + string(ENVVAR_KEY)).reset()
			optsText.write("  Add environment variable").nl()
		}

		for _, opt := range cmd.Options {
			if !opt.isFlag() {
				continue
			}

			dim := (app.lastPrefix != 0 && opt.prefix != app.lastPrefix)
			flags := strings.Join(opt.Flags, ", ")

			if dim {
				optsText.dim()
			}

			optsText.color(KEY_COLOR)
			optsText.bold().write(" " + string(opt.prefix) + string(opt.key)).unbold()
			optsText.nocolor()
			optsText.write("  " + opt.Help)

			if cmd.isOptionEnabled(opt) {
				optsText.italic().color(ARG_ON_COLOR)
			}

			optsText.dim().write(" (" + flags)

			metavar := "value"
			if opt.Metavar != "" {
				metavar = opt.Metavar
			}

			sep := " "
			if opt.Separator != "" {
				sep = opt.Separator
			}

			switch opt.getType() {
			case FLAG_TYPE_VALUE:
				optsText.write(sep)
				optsText.write("<" + metavar + ">)")
			case FLAG_TYPE_VALUE_OPTIONAL:
				// The string to display is "[$metavar]", but an extra "[" needs to be
				// added in order to prevent tview from interpreting it as a color tag.
				optsText.write(sep)
				optsText.write("[" + metavar + "[])")
			case FLAG_TYPE_TOGGLE:
				optsText.write(")")
			}

			if opt.Repeatable {
				// See comment above
				optsText.write(" [repeatable[]")
			}

			optsText.reset().nl()
		}

		for _, opt := range cmd.Options {
			if !opt.isArgument() {
				continue
			}

			if app.lastPrefix != 0 {
				optsText.dim()
			}

			metavar := "value"
			if opt.Metavar != "" {
				metavar = opt.Metavar
			}

			optsText.color(KEY_COLOR).bold().write("  " + string(opt.key)).unbold().nocolor()
			optsText.write("  " + opt.Help)

			if cmd.isOptionEnabled(opt) {
				optsText.italic().color(ARG_ON_COLOR)
			}

			optsText.dim().write(" (<" + metavar + ">)")

			if opt.Repeatable {
				optsText.write(" [repeatable[]")
			}

			optsText.reset().nl()
		}

		if i < len(app.enabledCommands)-1 {
			optsText.nl()
		}
	}

	count := app.ui.optionsPages.GetPageCount()
	for i := 0; i < count; i++ {
		app.ui.optionsPages.RemovePage(strconv.Itoa(i))
	}

	for i := 0; i < optsText.pagesCount(); i++ {
		view := tview.NewTextView()
		view.SetDynamicColors(true)
		view.SetWrap(false)
		view.SetText(optsText.page(i))

		app.ui.optionsPages.AddPage(strconv.Itoa(i), view, true, true)
	}

	if app.ui.optionsPages.HasPage(front) {
		app.ui.optionsPages.SwitchToPage(front)
	} else {
		app.ui.optionsPages.SwitchToPage("0")
	}
}

func (app *application) updateViews() {
	app.updateSubcommandsView()
	app.updateOptionsView()
	app.updateOptionsTitle()
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

	completions := []string{}
	for _, val := range os.Environ() {
		parts := strings.Split(val, "=")
		completions = append(completions, parts[0]+"=")
	}
	sort.Strings(completions)

	app.minibufferRead("value:", func(ok bool, val string) {
		if ok {
			parts := strings.Split(val, "=")
			if len(parts) == 1 || len(parts[0]) == 0 {
				app.showMessage("invalid environment variable format")
				return
			}
			app.environment = append(app.environment, val)
			app.cursor++
		}
	}, "", "VAR=VAL", completions)
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
				if !cmd.isOptionEnabled(opt) || opt.Repeatable {
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

func (app *application) promptOptionValue(cmd *subcommand, opt *option) {
	var completion []string

	if len(opt.Completion.Values) > 0 {
		completion = opt.Completion.Values
	}

	if opt.isFlag() && opt.isTemplate() {
		// Handle flags like --validate-<thing> (template)
		app.minibufferRead("flag:", func(ok bool, val string) {
			if !ok || !strings.HasPrefix(val, "-") {
				return
			}

			app.minibufferRead("value:", func(ok bool, val2 string) {
				if ok {
					app.addOptionValue(cmd, opt, val2, val)
				}
			}, opt.Default, opt.Placeholder, completion)

		}, opt.longFlag(), "", nil)

		return
	}

	app.minibufferRead("value:", func(ok bool, val string) {
		if ok {
			app.addOptionValue(cmd, opt, val, "")
		}
	}, opt.Default, opt.Placeholder, completion)
}

func (app *application) addOptionValue(cmd *subcommand, opt *option, val string, flag string) {
	cmd.optValues = append(cmd.optValues, &optionValue{opt: opt, value: val, flag: flag})
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
					if !cmd.isOptionEnabled(opt) {
						if opt.getType() == FLAG_TYPE_TOGGLE {
							app.addOptionValue(cmd, opt, "", "")
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

func (app *application) handlePagination(up bool) {
	front, _ := app.ui.optionsPages.GetFrontPage()
	count := app.ui.optionsPages.GetPageCount()

	index, _ := strconv.Atoi(front)
	if up {
		index--
	} else {
		index++
	}

	if index < 0 {
		index = 0
	} else if index > count-1 {
		index = count - 1
	}

	app.ui.optionsPages.SwitchToPage(strconv.Itoa(index))
}

func (app *application) handleHelpKey() {
	// Allow using the help key even if a prefix key was active,
	// for convenience.
	app.lastPrefix = 0
	app.helpActive = true
	app.ui.root.AddItem(app.ui.helpModal, 0, 0, true)
}

func (app *application) handleHelpClose() {
	app.ui.root.RemoveItem(app.ui.helpModal)
	app.helpActive = false
	app.tviewApp.SetFocus(app.ui.root)
}

func (app *application) handleFinishEditing() {
	command := app.currentCommand()
	fmt.Println(command)

	if !clipboard.Unsupported {
		clipboard.WriteAll(command)
		fmt.Println("(copied to clipboard)")
	}
}

func (app *application) initialize() {
	app.initialized = true
	app.showMessage("press %c for help, Ctrl-C to exit, ENTER to finish editing", HELP_KEY)
}

func (app *application) captureRootInput(event *tcell.EventKey) *tcell.EventKey {
	if app.minibufferActive {
		return event
	} else if app.helpActive {
		app.handleHelpClose()
		return nil
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
		app.onCloseCallback = app.handleFinishEditing
		app.tviewApp.Stop()
	case tcell.KeyLeft:
		app.cursor--
	case tcell.KeyRight:
		app.cursor++
	case tcell.KeyUp:
		fallthrough
	case tcell.KeyDown:
		app.handlePagination(key == tcell.KeyUp)
	case tcell.KeyRune:
		app.handlePrintableKey(event.Rune())
	}

	app.clampCursor()
	app.updateKeys()
	app.updateViews()

	if !app.initialized {
		app.initialize()
	}

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
	// Queue a key-press event so that captureRootInput is called immediately
	// after tview has finished setting up the application. This in turn allows
	// brief to do some further initialization (e.g. updating views for the first
	// time).
	app.tviewApp.QueueEvent(tcell.NewEventKey(CANCEL_KEY, 0, tcell.ModNone))

	if err := app.tviewApp.Run(); err != nil {
		panic(err)
	}

	if app.onCloseCallback != nil {
		app.onCloseCallback()
	}
}
