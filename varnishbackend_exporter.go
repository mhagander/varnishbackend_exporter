package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/version"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

type VarnishWrapper struct {
	conn net.Conn
}

func (v *VarnishWrapper) ReadResponse() (code int, response *string) {
	var status, length int

	headers, err := fmt.Fscanf(v.conn, "%03d %8d\n", &status, &length)
	if err != nil {
		fmt.Printf("Failed to scan header: %s\n", err)
		return -1, nil
	}

	if headers != 2 {
		fmt.Printf("Invalid number of headers: %d\n", headers)
		return -1, nil
	}

	buf := make([]byte, length+1)
	l, err := v.conn.Read(buf)
	if err != nil {
		fmt.Printf("Read from Varnish failed: %s\n", err)
		return -1, nil
	}

	if l != length+1 {
		fmt.Printf("Read %d, expected %d\n", l, length+1)
		return -1, nil
	}

	ret := string(buf[0 : len(buf)-1])
	return status, &ret
}

func (v *VarnishWrapper) Send(str string, args ...string) error {
	var buf = append([]string{str}, args...)
	body := fmt.Sprintf("%s\n", strings.Join(buf, " "))
	_, err := v.conn.Write([]byte(body))
	if err != nil {
		fmt.Printf("Write error: %s\n", err)
		return err
	}
	return nil
}

func (v *VarnishWrapper) CommandForSuccess(cmd string, args ...string) bool {
	err := v.Send(cmd, args...)
	if err != nil {
		return false
	}
	code, _ := v.ReadResponse()
	return (code == 200)
}

/* Prometheus counters */
var prombackends *prometheus.GaugeVec

var directorRegexp *regexp.Regexp = nil
var promlabels []string

/* Webserver goroutine that servers up the current metrics */
func httpServer(listenAddress string, metricsPath string) {
	http.Handle(metricsPath, promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
             <head><title>Varnishbackend Exporter</title></head>
             <body>
             <h1>Varnishbackend Exporter</h1>
             <p><a href='` + metricsPath + `'>Metrics</a></p>
             </body>
             </html>`))
	})
	http.ListenAndServe(listenAddress, nil)
}

var (
	debug = flag.Bool("debug", false, "Print debugging information.")
)

func Debug(msg string) {
	if *debug {
		fmt.Println(msg)
	}
}

func main() {
	var (
		listenAddress   = flag.String("web.listen-address", ":9133", "Address to listen on for web interface and telemetry.")
		metricsPath     = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
		varnishPort     = flag.Int("varnish.port", 6082, "Port of Varnish to connect to")
		varnishSecret   = flag.String("varnish.secret", "/etc/varnish/secret", "Filename of varnish secret file")
		varnishInterval = flag.Int("varnish.interval", 15, "Varnish checking interval")
		directorReStr   = flag.String("directorre", "", "Regular expression extracting director name from backend name")
		showVersion     = flag.Bool("version", false, "Print version information.")
		resetBackends   = flag.Bool("varnish.reset", false, "Reset the backends list after each scan.")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version.Print("varnishbackend_exporter"))
		os.Exit(0)
	}

	if *directorReStr != "" {
		directorRegexp = regexp.MustCompile(*directorReStr)
		promlabels = []string{"state", "director"}
	} else {
		promlabels = []string{"state"}
	}

	secret, err := ioutil.ReadFile(*varnishSecret)
	if err != nil {
		fmt.Printf("Failed to read %s: %s\n", *varnishSecret, err)
		os.Exit(1)
	}

	prombackends = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "varnish_backend_state",
			Help: "varnish backend states",
		},
		promlabels,
	)
	prometheus.MustRegister(prombackends)

	// Http listener
	go httpServer(*listenAddress, *metricsPath)

	// Main loop to poll Varnish
	tcpAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf("localhost:%d", *varnishPort))
	if err != nil {
		fmt.Printf("Could not resolve address: %s\n", err)
		os.Exit(1)
	}

	first := true
	for {
		/* To make sure we don't flood things */
		if first {
			first = false
		} else {
			/* Rate limit */
			Debug("Sleeping 5 seconds before connecting")
			time.Sleep(5 * time.Second)
		}
		Debug("Connecting to Varnish")
		conn, err := net.DialTCP("tcp", nil, tcpAddr)
		if err != nil {
			fmt.Printf("Connection failed: %s\n", err.Error())
			continue
		}
		defer conn.Close()
		vadm := &VarnishWrapper{conn: conn}
		code, resp := vadm.ReadResponse()
		if code != 107 {
			fmt.Println("Varnish did not give authentication prompt.")
			continue
		}
		challenge := strings.Split(*resp, "\n")[0]
		response := sha256.Sum256([]byte(fmt.Sprintf("%s\n%s%s\n", challenge, secret, challenge)))
		if !vadm.CommandForSuccess("auth", hex.EncodeToString(response[:])) {
			fmt.Println("Failed to authenticate")
			continue
		}

		/*
		 * Now that we have a working connection, loop with the same
		 * connection for multiple commands.
		 */
		for {
			Debug("Getting list from Varnish")
			err := vadm.Send("backend.list")
			if err != nil {
				break
			}

			code, resp := vadm.ReadResponse()
			if code != 200 {
				fmt.Printf("Received code %d, expected 200\n", code)
				break
			}
			scanner := bufio.NewScanner(strings.NewReader(*resp))
			var healthy, sick int
			var labelhealthy, labelsick, labelall map[string]int
			if directorRegexp != nil {
				labelhealthy = make(map[string]int)
				labelsick = make(map[string]int)
				labelall = make(map[string]int)
			} else {
				healthy = 0
				sick = 0
			}

			for scanner.Scan() {
				t := scanner.Text()
				if strings.HasPrefix(t, "Backend name ") {
					continue
				}
				fields := strings.Fields(t)

				if directorRegexp != nil {
					var lbl string
					m := directorRegexp.FindStringSubmatch(fields[0])
					if m != nil && len(m) > 1 {
						lbl = m[1]
					} else {
						lbl = "unknown"
					}
					labelall[lbl] = 1
					if fields[1] != "sick" && fields[2] == "Healthy" {
						labelhealthy[lbl]++
					} else {
						labelsick[lbl]++
					}
				} else {
					if fields[1] != "sick" && fields[2] == "Healthy" {
						healthy++
					} else {
						sick++
					}
				}
			}

			if *resetBackends {
				prombackends.Reset()
			}

			if directorRegexp != nil {
				for k := range labelall {
					prombackends.With(prometheus.Labels{"state": "healthy", "director": k}).Set(float64(labelhealthy[k]))
					prombackends.With(prometheus.Labels{"state": "sick", "director": k}).Set(float64(labelsick[k]))
				}
			} else {
				prombackends.With(prometheus.Labels{"state": "healthy"}).Set(float64(healthy))
				prombackends.With(prometheus.Labels{"state": "sick"}).Set(float64(sick))
			}

			Debug(fmt.Sprintf("Sleeping for %d seconds.", *varnishInterval))
			time.Sleep(time.Duration(*varnishInterval) * time.Second)
		}

		conn.Close()
	}
}
