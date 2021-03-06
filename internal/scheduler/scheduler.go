package scheduler

import (
	"encoding/base64"
	"fmt"
	"github.com/creekorful/trandoshan/api"
	"github.com/creekorful/trandoshan/internal/messaging"
	"github.com/creekorful/trandoshan/internal/util/logging"
	natsutil "github.com/creekorful/trandoshan/internal/util/nats"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
	"github.com/urfave/cli/v2"
	"github.com/xhit/go-str2duration/v2"
	"net/url"
	"strings"
	"time"
)

// GetApp return the scheduler app
func GetApp() *cli.App {
	return &cli.App{
		Name:    "tdsh-scheduler",
		Version: "0.4.0",
		Usage:   "Trandoshan scheduler process",
		Flags: []cli.Flag{
			logging.GetLogFlag(),
			&cli.StringFlag{
				Name:     "nats-uri",
				Usage:    "URI to the NATS server",
				Required: true,
			},
			&cli.StringFlag{
				Name:     "api-uri",
				Usage:    "URI to the API server",
				Required: true,
			},
			&cli.StringFlag{
				Name:  "refresh-delay",
				Usage: "Duration before allowing crawl of existing resource (none = never)",
			},
		},
		Action: execute,
	}
}

func execute(ctx *cli.Context) error {
	logging.ConfigureLogger(ctx)

	log.Info().Str("ver", ctx.App.Version).Msg("Starting tdsh-scheduler")

	log.Debug().Str("uri", ctx.String("nats-uri")).Msg("Using NATS server")
	log.Debug().Str("uri", ctx.String("api-uri")).Msg("Using API server")

	refreshDelay := parseRefreshDelay(ctx.String("refresh-delay"))
	if refreshDelay != -1 {
		log.Debug().Stringer("delay", refreshDelay).Msg("Existing resources will be crawled again")
	} else {
		log.Debug().Msg("Existing resources will NOT be crawled again")
	}

	// Create the API client
	apiClient := api.NewClient(ctx.String("api-uri"))

	// Create the NATS subscriber
	sub, err := natsutil.NewSubscriber(ctx.String("nats-uri"))
	if err != nil {
		return err
	}
	defer sub.Close()

	log.Info().Msg("Successfully initialized tdsh-scheduler. Waiting for URLs")

	if err := sub.QueueSubscribe(messaging.URLFoundSubject, "schedulers", handleMessage(apiClient, refreshDelay)); err != nil {
		return err
	}

	return nil
}

func handleMessage(apiClient api.Client, refreshDelay time.Duration) natsutil.MsgHandler {
	return func(nc *nats.Conn, msg *nats.Msg) error {
		var urlMsg messaging.URLFoundMsg
		if err := natsutil.ReadJSON(msg, &urlMsg); err != nil {
			return err
		}

		log.Debug().Str("url", urlMsg.URL).Msg("Processing URL")

		u, err := url.Parse(urlMsg.URL)
		if err != nil {
			log.Err(err).Msg("Error while parsing URL")
			return err
		}

		// Make sure URL is valid .onion
		if !strings.Contains(u.Host, ".onion") {
			log.Debug().Stringer("url", u).Msg("URL is not a valid hidden service")
			return err
		}

		// If we want to allow re-schedule of existing crawled resources we need to retrieve only resources
		// that are newer than now-refreshDelay.
		endDate := time.Time{}
		if refreshDelay != -1 {
			endDate = time.Now().Add(-refreshDelay)
		}

		b64URI := base64.URLEncoding.EncodeToString([]byte(u.String()))
		urls, _, err := apiClient.SearchResources(b64URI, "", time.Time{}, endDate, 1, 1)
		if err != nil {
			log.Err(err).Msg("Error while searching URL")
			return err
		}

		// No matches: schedule!
		if len(urls) == 0 {
			log.Debug().Stringer("url", u).Msg("URL should be scheduled")
			if err := natsutil.PublishMsg(nc, &messaging.URLTodoMsg{URL: urlMsg.URL}); err != nil {
				return fmt.Errorf("error while publishing URL: %s", err)
			}
		} else {
			log.Trace().Stringer("url", u).Msg("URL should not be scheduled")
		}

		return nil
	}
}

func parseRefreshDelay(delay string) time.Duration {
	if delay == "" {
		return -1
	}

	val, err := str2duration.ParseDuration(delay)
	if err != nil {
		return -1
	}

	return val
}
