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
	optionsTextView     *tview.TextView
	minibuffer          *tview.InputField
	messagesTextView    *tview.TextView
	root                *tview.Flex
}

type uiText struct {
	builder *strings.Builder
	flags   map[rune]struct{}
	color_   string
}

func NewUIText() *uiText {
	return &uiText{
		builder: &strings.Builder{},
		flags:   make(map[rune]struct{}),
	}
}

func (txt *uiText) write(s string) *uiText {
	_, err := txt.builder.WriteString(s)
	if err != nil {
		panic(err)
	}
	return txt
}

func (txt *uiText) String() string {
	return txt.builder.String()
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

func (txt *uiText) color(c string) *uiText {
	txt.color_ = c
	txt.writeFlags()
	return txt
}

func (txt *uiText) nocolor() *uiText {
	return txt.color("")
}

func regionInt(id int, contents string) string {
	return region(strconv.Itoa(id), contents)
}

func region(label, contents string) string {
	return fmt.Sprintf("[\"%v\"]%v[\"\"]", label, contents)
}

func newUserInterface() *userInterface {
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

	optionsTextView := tview.NewTextView()
	optionsTextView.SetDynamicColors(true)

	optionsFlex.AddItem(optionsTextView, 0, 1, false)

	bottomFlex := tview.NewFlex()

	bottomFlex.AddItem(subcommandsFlex, 0, 1, false)
	bottomFlex.AddItem(optionsFlex, 0, 2, false)

	messagesTextView := tview.NewTextView()

	root := tview.NewFlex().SetDirection(tview.FlexRow)

	root.AddItem(topFlex, 0, 1, false)
	root.AddItem(bottomFlex, 0, 4, false)
	root.AddItem(messagesTextView, 1, 0, false)

	minibuffer := tview.NewInputField()

	return &userInterface{
		root:                root,
		cmdPreviewTextView:  cmdPreviewTextView,
		subcommandsTextView: subcommandsTextView,
		optionsTextView:     optionsTextView,
		minibuffer:          minibuffer,
		messagesTextView:    messagesTextView,
	}
}
