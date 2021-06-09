package main

import (
	"bufio"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/facebookgo/freeport"
	"github.com/gobuild/log"
	"github.com/gorilla/mux"
	accesslog "github.com/mash/go-accesslog"
	"github.com/nanoscopic/wdaproxy/web"
	flag "github.com/ogier/pflag"
	_ "github.com/shurcooL/vfsgen"
)

func init() {
	log.SetFlags(log.Lshortfile | log.LstdFlags)
}

var (
	version        = "develop"
	lisPort        = 8100
	wdaPort        = 8100
	pWda           string
	udid           string
	yosemiteServer string
	yosemiteGroup  string
	debug          bool
	iosversion     string
	iosDeploy      string
	mobileDevice   string

	rt = mux.NewRouter()
	//udidNames = map[string]string{}
)

type statusResp struct {
	Value     map[string]interface{} `json:"value,omitempty"`
	SessionId string                 `json:"sessionId,omitempty"`
	Status    int                    `json:"status"`
}

func getUdid() string {
	if udid != "" {
		return udid
	}
	output, err := exec.Command("idevice_id", "-l").Output()
	if err != nil {
		panic(err)
	}
	return strings.TrimSpace(string(output))
}

func assetsContent(name string) string {
	fd, err := web.Assets.Open(name)
	if err != nil {
		panic(err)
	}
	data, err := ioutil.ReadAll(fd)
	if err != nil {
		panic(err)
	}
	return string(data)
}

type Device struct {
	Udid         string `json:"serial"`
	Manufacturer string `json:"manufacturer"`
}

// LocalIP returns the non loopback local IP of the host
func LocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, address := range addrs {
		// check the address type and if it is not a loopback the display it
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return ""
}

func main() {
	showVer := flag.BoolP("version", "v", false, "Print version")
	flag.IntVarP(&lisPort, "port", "p", 8100, "Proxy listen port")
	flag.IntVarP(&wdaPort, "wdaport", "q", 8100, "Upstream WDA port")
	flag.StringVarP(&udid, "udid", "u", "", "device udid")
	flag.StringVarP(&pWda, "wda", "W", "", "WebDriverAgent project directory [optional]")
	flag.BoolVarP(&debug, "debug", "d", false, "Open debug mode")
	flag.StringVarP(&iosversion, "iosversion", "V", "", "IOS Version")
	flag.StringVarP(&iosDeploy, "iosDeploy", "I", "", "ios-deploy path")
	flag.StringVarP(&mobileDevice, "mobileDevice", "M", "", "mobiledevice path")

	// flag.StringVarP(&yosemiteServer, "yosemite-server", "S",
	//     os.Getenv("YOSEMITE_SERVER"),
	//     "server center(not open source yet")
	// flag.StringVarP(&yosemiteGroup, "yosemite-group", "G",
	//     "everyone",
	//     "server center group")
	flag.Parse()
	if udid == "" {
		udid = getUdid()
	}

	if *showVer {
		println(version)
		return
	}

	lis, err := net.Listen("tcp", ":"+strconv.Itoa(lisPort))
	if err != nil {
		log.Fatal(err)
	}

	// if yosemiteServer != "" {
	//     mockIOSProvider()
	// }

	errC := make(chan error)
	freePort, err := freeport.Get()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("freeport %d", freePort)

	go func() {
		log.Printf("launch tcp-proxy, listen on %d", lisPort)
		targetURL, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(freePort))
		rt.HandleFunc("/wd/hub/{path:.*}", NewAppiumProxyHandlerFunc(targetURL))
		rt.HandleFunc("/{path:.*}", NewReverseProxyHandlerFunc(targetURL))
		err := http.Serve(lis, accesslog.NewLoggingHandler(rt, HTTPLogger{}))
		fmt.Printf("http failure\n")
		errC <- err
	}()
	go func() {
		log.Printf("launch iproxy (udid: %s)", strconv.Quote(udid))

		var c *exec.Cmd
		if mobileDevice != "" {
			c = exec.Command(mobileDevice, "tunnel", "-u", udid, strconv.Itoa(freePort), strconv.Itoa(wdaPort))
		} else {
			iproxyVersion := iproxy_version()
			if iproxyVersion == 1 {
				c = exec.Command("/usr/local/bin/iproxy", strconv.Itoa(freePort), strconv.Itoa(wdaPort))
				if udid != "" {
					c.Args = append(c.Args, udid)
				}
			} else if iproxyVersion == 2 {
				c = exec.Command("/usr/local/bin/iproxy", "-s", "127.0.0.1", strconv.Itoa(freePort)+":"+strconv.Itoa(wdaPort))
				if udid != "" {
					c.Args = append(c.Args, "-u", udid)
				}
			}
		}

		c.Stdout = os.Stdout
		c.Stderr = os.Stderr

		err := c.Run()
		fmt.Printf("iproxy failure\n")
		errC <- err
	}()
	go func(udid string) {
		if pWda == "" {
			return
		}
		// device name
		/*var deviceName string
		  if iosDeploy != "" {
		      nameBytes, _ := exec.Command( iosDeploy, "-u", udid).Output()
		      deviceName = strings.TrimSpace(string(nameBytes))
		  } else {
		      nameBytes, _ := exec.Command("/usr/local/bin/idevicename", "-u", udid).Output()
		      deviceName = strings.TrimSpace(string(nameBytes))
		  }
		  udidNames[udid] = deviceName
		  log.Printf("device name: %s", deviceName)*/

		log.Printf("launch WebDriverAgent(dir=%s)", pWda)

		var c *exec.Cmd
		if fileExists("WebDriverAgent.xcodeproj") {
			c = exec.Command("xcodebuild",
				"-verbose",
				"-project", "WebDriverAgent.xcodeproj",
				"-scheme", "WebDriverAgentRunner",
				"-destination", "id="+udid, "test-without-building") // test-without-building
		} else {
			xctestrunFile := findXctestrun(pWda)
			if xctestrunFile == "" {
				log.Fatal("Could not find WebDriverAgent.xcodeproj or xctestrun of sufficient version")
			}
			c = exec.Command("xcodebuild",
				"test-without-building",
				"-xctestrun", xctestrunFile,
				"-destination", "id="+udid)
		}
		c.Dir, _ = filepath.Abs(pWda)
		// Test Suite 'All tests' started at 2017-02-27 15:55:35.263
		// Test Suite 'WebDriverAgentRunner.xctest' started at 2017-02-27 15:55:35.266
		// Test Suite 'UITestingUITests' started at 2017-02-27 15:55:35.267
		// Test Case '-[UITestingUITests testRunner]' started.
		// t =     0.00s     Start Test at 2017-02-27 15:55:35.270
		// t =     0.01s     Set Up
		pipeReader, writer := io.Pipe()
		c.Stdout = writer
		c.Stderr = writer
		c.Stdin = os.Stdin

		portLine := fmt.Sprintf("USE_PORT=%d", wdaPort)
		c.Env = append(os.Environ(), portLine)

		bufrd := bufio.NewReader(pipeReader)
		if err = c.Start(); err != nil {
			log.Fatal(err)
		}

		// close writers when xcodebuild exit
		go func() {
			c.Wait()
			writer.Close()
		}()

		lineStr := ""
		for {
			line, isPrefix, err := bufrd.ReadLine()
			if isPrefix {
				lineStr = lineStr + string(line)
				continue
			} else {
				lineStr = string(line)
			}
			lineStr = strings.TrimSpace(string(line))

			if debug {
				fmt.Printf("[WDA] %s\n", lineStr)
			}
			if err != nil {
				log.Fatal("[WDA] exit", err)
			}
			if strings.Contains(lineStr, "Successfully wrote Manifest cache to") {
				log.Println("[WDA] test ipa successfully generated")
			}
			if strings.HasPrefix(lineStr, "Test Case '-[UITestingUITests testRunner]' started") {
				log.Println("[WDA] successfully started")
			}
			lineStr = "" // reset str
		}
	}(udid)

	log.Printf("Open webbrower with http://%s:%d", LocalIP(), lisPort)
	err2 := <-errC
	log.Fatalf("error %s\n", err2)
}

func iproxy_version() int {
	output, _ := exec.Command("/usr/local/bin/iproxy", "-h").Output()
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "LOCAL_PORT:DEVICE_PORT") {
			return 2
		}
	}
	return 1
}

func findXctestrun(folder string) string {
	var files []string
	err := filepath.Walk(folder, func(file string, info os.FileInfo, err error) error {
		if info.IsDir() && folder != file {
			return filepath.SkipDir
		}
		files = append(files, file)
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	versionMatch := false
	var findMajor int64 = 0
	var findMinor int64 = 0
	var curMajor int64 = 100
	var curMinor int64 = 100
	if iosversion != "" {
		parts := strings.Split(iosversion, ".")
		findMajor, _ = strconv.ParseInt(parts[0], 10, 64)
		findMinor, _ = strconv.ParseInt(parts[1], 10, 64)
		versionMatch = true
	}

	xcFile := ""
	for _, file := range files {
		if !strings.HasSuffix(file, ".xctestrun") {
			continue
		}

		if !versionMatch {
			xcFile = file
			break
		}

		r := regexp.MustCompile(`iphoneos([0-9]+)\.([0-9]+)`)
		fileParts := r.FindSubmatch([]byte(file))
		fileMajor, _ := strconv.ParseInt(string(fileParts[1]), 10, 64)
		fileMinor, _ := strconv.ParseInt(string(fileParts[2]), 10, 64)

		// Find the smallest file version greater than or equal to the ios version
		// Golang line continuation for long boolean expressions is horrible. :(

		// Checked file version smaller than current file version
		// &&
		// Checked file version greater or equal to ios version
		if (fileMajor < curMajor || (fileMajor == curMajor && fileMinor <= curMinor)) &&
			(fileMajor > findMajor || (fileMajor == findMajor && fileMinor >= findMinor)) {
			curMajor = fileMajor
			curMinor = fileMinor
			xcFile = file
		}
	}
	return xcFile
}

func fileExists(filename string) bool {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}
