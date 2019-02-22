package main

import (
	"context"
	"fmt"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/terorie/od-database-crawler/fasturl"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"time"
)

var configFile string

var rootCmd = cobra.Command {
	Use: "od-database-crawler",
	Version: "1.2.2",
	Short: "OD-Database Go crawler",
	Long: helpText,
	PersistentPreRunE: preRun,
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		exitHooks.Execute()
	},
}

var serverCmd = cobra.Command {
	Use: "server",
	Short: "Start crawl server",
	Long: "Connect to the OD-Database and contribute to the database\n" +
		"by crawling the web for open directories!",
	Run: cmdBase,
}

var crawlCmd = cobra.Command {
	Use: "crawl",
	Short: "Crawl an URL",
	Long: "Crawl the URL specified.\n" +
		"Results will not be uploaded to the database,\n" +
		"they're saved under crawled/0.json instead.\n" +
		"Primarily used for testing and benchmarking.",
	RunE: cmdCrawler,
	Args: cobra.ExactArgs(1),
}

var exitHooks Hooks

func init() {
	rootCmd.AddCommand(&crawlCmd)
	rootCmd.AddCommand(&serverCmd)

	prepareConfig()
}

func preRun(cmd *cobra.Command, args []string) error {
	if err := os.MkdirAll("crawled", 0755);
		err != nil { panic(err) }

	if err := os.MkdirAll("queue", 0755);
		err != nil { panic(err) }

	return nil
}

func main() {
	err := rootCmd.Execute()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func cmdBase(_ *cobra.Command, _ []string) {
	onlineMode = true
	readConfig()

	appCtx, soft := context.WithCancel(context.Background())
	forceCtx, hard := context.WithCancel(context.Background())
	go hardShutdown(forceCtx)
	go listenCtrlC(soft, hard)

	inRemotes := make(chan *OD)
	go Schedule(appCtx, inRemotes)

	ticker := time.NewTicker(config.Recheck)
	defer ticker.Stop()
	for {
		select {
		case <-appCtx.Done():
			goto shutdown
		case <-ticker.C:
			t, err := FetchTask()
			if err != nil {
				logrus.WithError(err).
					Error("Failed to get new task")
				if !sleep(viper.GetDuration(ConfCooldown), appCtx) {
					goto shutdown
				}
				continue
			}
			if t == nil {
				// No new task
				if atomic.LoadInt32(&numActiveTasks) == 0 {
					logrus.Info("Waiting …")
				}
				continue
			}

			var baseUri fasturl.URL
			err = baseUri.Parse(t.Url)
			if urlErr, ok := err.(*fasturl.Error); ok && urlErr.Err == fasturl.ErrUnknownScheme {
				// Not an error
				err = nil
				// TODO FTP crawler
				continue
			} else if err != nil {
				logrus.WithError(err).
					Error("Failed to get new task")
				time.Sleep(viper.GetDuration(ConfCooldown))
				continue
			}
			ScheduleTask(inRemotes, t, &baseUri)
		}
	}

shutdown:
	globalWait.Wait()
}

func cmdCrawler(_ *cobra.Command, args []string) error {
	onlineMode = false
	readConfig()

	arg := args[0]
	// https://github.com/golang/go/issues/19779
	if !strings.Contains(arg, "://") {
		arg = "http://" + arg
	}
	var u fasturl.URL
	err := u.Parse(arg)
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	if err != nil { return err }

	// TODO Graceful shutdown
	forceCtx := context.Background()

	inRemotes := make(chan *OD)
	go Schedule(forceCtx, inRemotes)

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	task := Task {
		WebsiteId: 0,
		Url: u.String(),
	}
	ScheduleTask(inRemotes, &task, &u)

	// Wait for all jobs to finish
	globalWait.Wait()

	return nil
}

func listenCtrlC(soft, hard context.CancelFunc) {
	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt)

	<-c
	logrus.Info(">>> Shutting down crawler... <<<")
	soft()

	<-c
	logrus.Warning(">>> Force shutdown! <<<")
	hard()
}

func hardShutdown(c context.Context) {
	<-c.Done()
	os.Exit(1)
}

func sleep(d time.Duration, c context.Context) bool {
	select {
	case <-time.After(d):
		return true
	case <-c.Done():
		return false
	}
}
