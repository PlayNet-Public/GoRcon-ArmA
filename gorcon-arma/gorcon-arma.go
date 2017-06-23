package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"runtime"

	raven "github.com/getsentry/raven-go"
	bercon "github.com/playnet-public/gorcon-arma/bercon/client"
	"github.com/playnet-public/gorcon-arma/common"
	"github.com/playnet-public/gorcon-arma/rcon"
	"github.com/playnet-public/gorcon-arma/scheduler"
	"github.com/playnet-public/gorcon-arma/watcher"

	"github.com/golang/glog"
	"github.com/spf13/viper"
)

const (
	parameterMaxprocs   = "maxprocs"
	parameterConfigPath = "configPath"
	parameterDevBuild   = "devbuild"
)

var (
	maxprocsPtr   = flag.Int(parameterMaxprocs, runtime.NumCPU(), "max go procs")
	configPathPtr = flag.String(parameterConfigPath, ".", "config parent folder")
	devBuildPtr   = flag.Bool(parameterDevBuild, false, "set dev build mode")
)

var cfg *viper.Viper

func main() {
	defer glog.Flush()
	glog.CopyStandardLogTo("info")
	flag.Parse()
	fmt.Println("-- PlayNet GoRcon-ArmA - OpenSource Server Manager --")
	fmt.Println("Version:", version)
	fmt.Println("SourceCode: http://bit.ly/gorcon-code")
	fmt.Println("Tasks: http://bit.ly/gorcon-issues")
	fmt.Println("")
	fmt.Println("This project is work in progress - Use at your own risk")
	fmt.Println("--")
	fmt.Println("")
	fmt.Printf("Using %d go procs\n", *maxprocsPtr)
	runtime.GOMAXPROCS(*maxprocsPtr)

	raven.CapturePanicAndWait(func() {
		if err := do(); err != nil {
			glog.Fatal(err)
			raven.CaptureErrorAndWait(err, map[string]string{"isFinal": "true"})
		}
	}, nil)
}

func do() (err error) {
	cfg = getConfig()

	if !*devBuildPtr {
		raven.SetDSN(cfg.GetString("playnet.sentry"))
		raven.SetIncludePaths([]string{
			"github.com/playnet-public/gorcon-arma/",
		})
		//raven.SetRelease(version)
	}

	useSched := cfg.GetBool("scheduler.enabled")
	useWatch := cfg.GetBool("watcher.enabled")
	logToConsole := cfg.GetBool("watcher.logToConsole")
	useRcon := cfg.GetBool("arma.enabled")
	var sched *scheduler.Scheduler
	var watch *watcher.Watcher
	var client *rcon.Client

	cr, cw := io.Pipe()
	if logToConsole {
		go streamConsole(cr)
	}
	quit := make(chan int)

	if useSched {
		sched, err = newScheduler()
		if err != nil {
			return
		}
	}

	if useWatch {
		var stderr, stdout io.Writer
		if logToConsole {
			stderr, stdout = cw, cw
		}
		watch, err = newProcWatch(stderr, stdout)
		if err != nil {
			return
		}
		if useSched {
			sched.UpdateFuncs(watch.InjectExtFuncs(sched.Funcs))
		}
	}

	if useRcon {
		client, err = newRcon()
		if err != nil {
			return
		}
		client.Exec([]byte("say -1 PlayNet GoRcon-ArmA Connected"), nil)
		if useSched {
			sched.UpdateFuncs(client.InjectExtFuncs(sched.Funcs))
		}
	}

	//Finish Func and Event Collection and start Scheduling
	sched.BuildEvents()
	sched.Start()

	q := <-quit
	if q == 1 {
		return nil
	}
	return nil
}

func newRcon() (*rcon.Client, error) {
	beIP := cfg.GetString("arma.ip")
	bePort := cfg.GetString("arma.port")
	bePassword := cfg.GetString("arma.password")
	beKeepAliveTimer := cfg.GetInt("arma.keepAliveTimer")
	beKeepAliveTolerance := cfg.GetInt64("arma.keepAliveTolerance")

	beCred := rcon.Credentials{
		Username: "",
		Password: bePassword,
	}

	beConAddr, err := net.ResolveUDPAddr("udp", beIP+":"+bePort)
	if err != nil {
		return nil, err
	}

	beCon := rcon.Connection{
		Addr:               beConAddr,
		KeepAliveTimer:     beKeepAliveTimer,
		KeepAliveTolerance: beKeepAliveTolerance,
	}
	beCl := bercon.New(beCon, beCred)
	rc := rcon.NewClient(
		beCl.WatcherLoop,
		beCl.Disconnect,
		beCl.Exec,
		beCl.AttachEvents,
		beCl.AttachChat,
	)
	glog.Infoln("Establishing Connection to Server")
	err = rc.Connect()
	if err != nil {
		return nil, err
	}
	return rc, nil
}

func newProcWatch(stderr, stdout io.Writer) (w *watcher.Watcher, err error) {
	execPath := cfg.GetString("watcher.exec")
	execDir := cfg.GetString("watcher.dir")
	if execDir == "" {
		execDir = path.Dir(execPath)
	}
	execParam := cfg.GetStringSlice("watcher.params")

	proc := watcher.Process{
		ExecPath:  execPath,
		ExecDir:   execDir,
		ExecParam: execParam,
		StdErr:    stderr,
		StdOut:    stdout,
	}

	w = watcher.New(proc)
	err = w.Exec()
	if err != nil {
		return
	}
	if cfg.GetBool("watcher.autoRestart") {
		w.Watch(w.RestartAndWatch)
	}
	return w, nil
}

func logCmd(cmd string) { glog.Infoln(cmd) }

func newScheduler() (sched *scheduler.Scheduler, err error) {
	scPath := cfg.GetString("scheduler.path")
	schedule, err := scheduler.ReadSchedule(scPath)
	if err != nil {
		return
	}
	funcs := make(common.ScheduleFuncs)
	funcs["log"] = logCmd
	sched = scheduler.New(schedule, funcs)
	return
}

func streamConsole(consoleOut io.Reader) error {
	consoleScanner := bufio.NewScanner(consoleOut)
	for consoleScanner.Scan() {
		glog.Infoln(consoleScanner.Text())
	}
	if err := consoleScanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "There was an error with the consoleScanner", err)
		return err
	}
	return nil
}

func getConfig() *viper.Viper {
	cfg := viper.New()
	cfg.SetConfigName("config")
	cfg.AddConfigPath(*configPathPtr)

	glog.V(1).Infof("Reading Config")

	err := cfg.ReadInConfig()
	if err != nil {
		message := fmt.Sprintf("Loading Config failed with Error: %v", err.Error())
		glog.Errorln(message)
		panic(message)
	}
	return cfg
}
