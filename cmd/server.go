package cmd

import (
	"flag"
	"fmt"
	"strings"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/fhdoc/go-pingdom/pingdom"
	"github.com/spf13/cobra"
	"github.com/marpaia/graphite-golang"
)

var (
	serverCmd = &cobra.Command{
		Use:   "server [username] [password] [api-key] [email-owner] [graphite-host] [graphite-port]",
		Short: "Start the HTTP server",
		Run:   serverRun,
	}

	waitSeconds int
	port        int

	pingdomUp = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "pingdom_up",
		Help: "Whether the last pingdom scrape was successfull (1: up, 0: down)",
	})

	pingdomCheckStatus = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pingdom_check_status",
		Help: "The current status of the check (0: up, 1: unconfirmed_down, 2: down, -1: paused, -2: unknown)",
	}, []string{"id", "name", "hostname", "resolution", "paused", "tags"})

	pingdomCheckResponseTime = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pingdom_check_response_time",
		Help: "The response time of last test in milliseconds",
	}, []string{"id", "name", "hostname", "resolution", "paused", "tags"})
)

func init() {
	RootCmd.AddCommand(serverCmd)

	serverCmd.Flags().IntVar(&waitSeconds, "wait", 60, "time (in seconds) between accessing the Pingdom  API")
	serverCmd.Flags().IntVar(&port, "port", 8000, "port to listen on")

	prometheus.MustRegister(pingdomUp)
	prometheus.MustRegister(pingdomCheckStatus)
	prometheus.MustRegister(pingdomCheckResponseTime)
}

func sleep() {
	time.Sleep(time.Second * time.Duration(waitSeconds))
}

func serverRun(cmd *cobra.Command, args []string) {
	flag.Parse()

	if len(cmd.Flags().Args()) != 6 {
		cmd.Help()
		os.Exit(1)
	}

	client := pingdom.NewMultiUserClient(
		flag.Arg(1),
		flag.Arg(2),
		flag.Arg(3),
		flag.Arg(4), //"r.hanna@criteo.com"
	)

	graphPort, _ := strconv.Atoi(flag.Arg(6))

	go func() {
		for {
			checks, err := client.Checks.List()
			if err != nil {
				log.Println("Error getting checks ", err)
				pingdomUp.Set(0)

				sleep()
				continue
			}
			pingdomUp.Set(1)

			for _, check := range checks {
				id := strconv.Itoa(check.ID)

				var status float64
				switch check.Status {
				case "unknown":
					status = -2
				case "paused":
					status = -1
				case "up":
					status = 0
				case "unconfirmed_down":
					status = 1
				case "down":
					status = 2
				default:
					status = 100
				}

				resolution := strconv.Itoa(check.Resolution)

				paused := strconv.FormatBool(check.Paused)
				// Pingdom library doesn't report paused correctly,
				// so calculate it off the status.
				if check.Status == "paused" {
					paused = "true"
				}
				
				checkResponse, _ := client.Checks.Read(check.ID)	

				var allTags []string 
				for _, value := range checkResponse.Tags {
					allTags = append(allTags, value["name"] )
				}

				dataTags := strings.Join(allTags, ",")

				pingdomCheckStatus.WithLabelValues(
					id,
					check.Name,
					check.Hostname,
					resolution,
					paused,
					dataTags,
				).Set(status)


				pingdomCheckResponseTime.WithLabelValues(
					id,
					check.Name,
					check.Hostname,
					resolution,
					paused,
					dataTags,
				).Set(float64(check.LastResponseTime))


        			Graphite, err := graphite.NewGraphite(flag.Arg(5), graphPort)
        			if err != nil {
                			log.Println("Error connecting graphite ", err)
        			} else {
					graphPath := "criteo.pingdom.CheckResponseTime." +check.Name+ ".value" 
					responseTime := fmt.Sprintf("%v", check.LastResponseTime)
					metric := graphite.NewMetric(graphPath, responseTime, checkResponse.LastTestTime)
					Graphite.SendMetric(metric)
					log.Println("Metric responseTime ", metric)
                                	graphPath = "criteo.pingdom.CheckStatus." +check.Name+ ".value"
					checkStatus := fmt.Sprintf("%v", status)
                                	metric = graphite.NewMetric(graphPath, checkStatus, checkResponse.LastTestTime)
					log.Println("Metric status ", metric)
                                	Graphite.SendMetric(metric)
					Graphite.Disconnect()
				}	
			}

			sleep()
		}
	}()

	go func() {
		intChan := make(chan os.Signal)
		termChan := make(chan os.Signal)

		signal.Notify(intChan, syscall.SIGINT)
		signal.Notify(termChan, syscall.SIGTERM)

		select {
		case <-intChan:
			log.Print("Received SIGINT, exiting")
			os.Exit(0)
		case <-termChan:
			log.Print("Received SIGTERM, exiting")
			os.Exit(0)
		}
	}()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "")
	})
	http.Handle("/metrics", prometheus.Handler())

	log.Print("Listening on port ", port)

	http.ListenAndServe(fmt.Sprintf(":%d", port), nil)
}
