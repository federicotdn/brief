package main

import (
	"fmt"
	"github.com/rivo/tview"
	"strconv"
	"strings"
)

type userInterface struct {
	cmdPreviewTextView  *tview.TextView
	subcommandsTextView *tview.TextView
	optionsPages        *tview.Pages
	optionsFlex         *tview.Flex
	minibuffer          *tview.InputField
	messagesTextView    *tview.TextView
	helpModal           *tview.Modal
	root                *tview.Flex
}

type uiText struct {
	builders      []*strings.Builder
	flags         map[rune]struct{}
	color_        string
	maxHeight     int
	currentHeight int
	i             int
	paginated     bool
}

const HELP_TEXT = `
Usage for brief:

Subcommands, when available, are shown on the left panel. Activate subcommands by pressing their corresponding keys. For example, given:

f  foo
b  bar

then the 'f' key would activate the foo subcommand, and 'b' would activate the bar subcommand.

Options are shown on the right panel. Activate flags by first pressing the corresponding prefix key ('-', '=' or '+') and letter. For example, given:

-t  test (--test)
=v  version (--version)

Then pressing '-' followed by 't' would enable the --test flag. Pressing '=' followed by 'v' would enable the --version flag.

Positional arguments are enabled by pressing their corresponding number key ('0', '9', etc).

More information available at:
https://github.com/federicotdn/brief

Press any key to dismiss this window.

`

func NewUIText(paginated bool, maxHeight int) *uiText {
	return &uiText{
		flags:         make(map[rune]struct{}),
		maxHeight:     maxHeight,
		currentHeight: 1,
		paginated:     paginated,
	}
}

func (txt *uiText) write(s string) *uiText {
	for len(txt.builders)-1 < txt.i {
		txt.builders = append(txt.builders, &strings.Builder{})
	}

	_, err := txt.builders[txt.i].WriteString(s)
	if err != nil {
		panic(err)
	}
	return txt
}

func (txt *uiText) nl() *uiText {
	if txt.paginated && txt.currentHeight == txt.maxHeight {
		txt.i++
		txt.currentHeight = 0
	} else {
		txt.write("\n")
		txt.currentHeight++
	}

	return txt
}

func (txt *uiText) pagesCount() int {
	return len(txt.builders)
}

func (txt *uiText) page(i int) string {
	s := txt.builders[i].String()
	return s
}

func (txt *uiText) writeFlags() {
	keys := make([]rune, len(txt.flags))
	i := 0
	for k := range txt.flags {
		keys[i] = k
		i++
	}

	txt.write(fmt.Sprintf("[-::-][%v::%v]", txt.color_, string(keys)))
}

func (txt *uiText) applyFlag(flag rune) *uiText {
	_, ok := txt.flags[flag]
	if ok {
		return txt
	}
	txt.flags[flag] = struct{}{}
	txt.writeFlags()
	return txt
}

func (txt *uiText) removeFlag(flag rune) *uiText {
	_, ok := txt.flags[flag]
	if !ok {
		return txt
	}
	delete(txt.flags, flag)
	txt.writeFlags()
	return txt
}

func (txt *uiText) bold() *uiText {
	return txt.applyFlag('b')
}

func (txt *uiText) unbold() *uiText {
	return txt.removeFlag('b')
}

func (txt *uiText) dim() *uiText {
	return txt.applyFlag('d')
}

func (txt *uiText) undim() *uiText {
	return txt.removeFlag('d')
}

func (txt *uiText) italic() *uiText {
	return txt.applyFlag('i')
}

func (txt *uiText) noitalic() *uiText {
	return txt.removeFlag('i')
}

func (txt *uiText) color(c string) *uiText {
	txt.color_ = c
	txt.writeFlags()
	return txt
}

func (txt *uiText) nocolor() *uiText {
	return txt.color("")
}

func (txt *uiText) reset() *uiText {
	return txt.undim().unbold().noitalic().nocolor()
}

func regionInt(id int, contents string) string {
	return region(strconv.Itoa(id), contents)
}

func region(label, contents string) string {
	return fmt.Sprintf("[\"%v\"]%v[\"\"]", label, contents)
}

func newUserInterface(subcommandsEnabled bool) *userInterface {
	topFlex := tview.NewFlex().SetDirection(tview.FlexRow)
	topFlex.SetBorder(true)
	topFlex.SetTitle("Command preview")
	topFlex.SetTitleAlign(tview.AlignLeft)

	cmdPreviewTextView := tview.NewTextView().SetTextAlign(tview.AlignCenter)
	cmdPreviewTextView.SetRegions(true)

	paddingTop := tview.NewBox()
	paddingBottom := tview.NewBox()

	topFlex.AddItem(paddingTop, 0, 1, false)
	topFlex.AddItem(cmdPreviewTextView, 0, 1, false)
	topFlex.AddItem(paddingBottom, 0, 1, false)

	subcommandsFlex := tview.NewFlex()
	subcommandsFlex.SetBorder(true)
	subcommandsFlex.SetTitle("Subcommands")
	subcommandsFlex.SetTitleAlign(tview.AlignLeft)

	subcommandsTextView := tview.NewTextView()
	subcommandsTextView.SetDynamicColors(true)

	subcommandsFlex.AddItem(subcommandsTextView, 0, 1, false)

	optionsFlex := tview.NewFlex()
	optionsFlex.SetBorder(true)
	optionsFlex.SetTitle("Options")
	optionsFlex.SetTitleAlign(tview.AlignLeft)

	optionsPages := tview.NewPages()
	optionsFlex.AddItem(optionsPages, 0, 1, false)

	bottomFlex := tview.NewFlex()

	if subcommandsEnabled {
		bottomFlex.AddItem(subcommandsFlex, 0, 1, false)
	}
	bottomFlex.AddItem(optionsFlex, 0, 2, false)

	messagesTextView := tview.NewTextView()

	root := tview.NewFlex().SetDirection(tview.FlexRow)

	root.AddItem(topFlex, 0, 1, false)
	root.AddItem(bottomFlex, 0, 4, false)
	root.AddItem(messagesTextView, 1, 0, false)

	minibuffer := tview.NewInputField()

	helpModal := tview.NewModal().AddButtons([]string{"Close"})
	helpModal.SetText(HELP_TEXT)

	return &userInterface{
		root:                root,
		cmdPreviewTextView:  cmdPreviewTextView,
		subcommandsTextView: subcommandsTextView,
		optionsPages:        optionsPages,
		optionsFlex:         optionsFlex,
		minibuffer:          minibuffer,
		messagesTextView:    messagesTextView,
		helpModal:           helpModal,
	}
}
