package main

import (
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/mattermost/mattermost/server/public/pluginapi/cluster"
	"github.com/pkg/errors"

	"github.com/leightonrodney/mattermost-plugin-followups/server/store"
)

// Plugin is the bridge between the Mattermost server and everything else in
// this package.
type Plugin struct {
	plugin.MattermostPlugin

	// client is the Mattermost server API, wrapped in the typed helper.
	client *pluginapi.Client

	// store owns the plugin's key-value records.
	store *store.Store

	// service holds the reminder rules.
	service *service

	// router serves the plugin's HTTP API.
	router *mux.Router

	// botID is the account reminders are delivered from.
	botID string

	// scheduler is the cluster-wide background job that delivers reminders.
	// Only one instance in the cluster runs the callback at a time.
	scheduler *cluster.Job

	// configuration holds the active settings.
	configuration configurationHolder
}

// botUsername is the account that delivers reminders. Using a dedicated bot
// rather than the system bot means an administrator can tell at a glance where
// a reminder came from, and that disabling it affects nothing else.
const botUsername = "followups"

// OnActivate wires the plugin up. Returning an error here leaves the plugin
// deactivated, so everything the plugin cannot work without belongs in it.
func (p *Plugin) OnActivate() error {
	p.client = pluginapi.NewClient(p.API, p.Driver)

	botID, err := p.client.Bot.EnsureBot(&model.Bot{
		Username:    botUsername,
		DisplayName: "Follow-ups",
		Description: "Delivers your reminders and follow-ups.",
	})
	if err != nil {
		return errors.Wrap(err, "failed to ensure the follow-ups bot")
	}
	p.botID = botID

	p.store = store.New(&p.client.KV)
	p.service = newService(p.store, &p.client.User, p.getConfiguration)

	p.router = p.newRouter()

	if commandErr := p.registerCommand(); commandErr != nil {
		return errors.Wrap(commandErr, "failed to register the /remind command")
	}

	// cluster.Schedule holds a cluster-wide lock for the duration of each run,
	// so exactly one node delivers at a time. Per-reminder claiming in the
	// service is what covers the case where that lock expires mid-run.
	job, jobErr := cluster.Schedule(p.API, "followups-scheduler", p.nextSchedulerWait, p.runScheduler)
	if jobErr != nil {
		return errors.Wrap(jobErr, "failed to schedule the reminder worker")
	}
	p.scheduler = job

	p.client.Log.Info("Follow-ups activated", "bot_user_id", botID)
	return nil
}

// OnDeactivate stops the scheduler and waits for an in-flight run to finish, so
// that the plugin does not leave a reminder half-delivered.
func (p *Plugin) OnDeactivate() error {
	if p.scheduler != nil {
		if err := p.scheduler.Close(); err != nil {
			p.client.Log.Error("Failed to stop the reminder worker", "error", err.Error())
		}
		p.scheduler = nil
	}
	return nil
}

// nextSchedulerWait decides how long to wait before the next scheduler run.
//
// Reading the configuration here rather than fixing the interval when the job
// is created is what lets an administrator change the interval without
// restarting the plugin: the new value is picked up on the next wait.
func (p *Plugin) nextSchedulerWait(now time.Time, metadata cluster.JobMetadata) time.Duration {
	interval := p.getConfiguration().SchedulerInterval()

	if metadata.LastFinished.IsZero() {
		return 0
	}

	elapsed := now.Sub(metadata.LastFinished)
	if elapsed >= interval {
		return 0
	}
	return interval - elapsed
}

// ExecuteCommand handles /remind.
func (p *Plugin) ExecuteCommand(_ *plugin.Context, args *model.CommandArgs) (*model.CommandResponse, *model.AppError) {
	response, err := p.handleCommand(args)
	if err != nil {
		p.client.Log.Error("Failed to execute command", "error", err.Error())
		return nil, model.NewAppError("ExecuteCommand", "followups.command.error", nil, err.Error(), http.StatusInternalServerError)
	}
	return response, nil
}

// ServeHTTP serves the plugin's own API, mounted by the server at
// /plugins/<plugin id>/.
func (p *Plugin) ServeHTTP(_ *plugin.Context, w http.ResponseWriter, r *http.Request) {
	p.router.ServeHTTP(w, r)
}

// logDebug logs only when the administrator has turned debug logging on. It
// exists so that call sites do not have to repeat the check.
func (p *Plugin) logDebug(message string, keyValuePairs ...any) {
	if !p.getConfiguration().EnableDebugLogging {
		return
	}
	p.client.Log.Debug(message, keyValuePairs...)
}
