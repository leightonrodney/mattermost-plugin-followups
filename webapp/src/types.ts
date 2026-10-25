// Shapes returned by the plugin's own HTTP API. They mirror the Go payload
// structs in server/api.go; a change on one side needs the matching change here.

export type ReminderStatus =
    | 'scheduled'
    | 'delivering'
    | 'delivered'
    | 'cancelled'
    | 'failed';

export type RecurrenceType = 'daily' | 'weekly' | 'monthly' | 'interval';

export interface Recurrence {
    type: RecurrenceType;
    interval?: number;

    /** Days of the week a weekly rule fires on, 0 for Sunday through 6 for Saturday. */
    weekdays?: number[];
    day_of_month?: number;
    hour: number;
    minute: number;
    every_seconds?: number;
}

export interface Reminder {
    id: string;
    message: string;

    /** Unix milliseconds. */
    due_at: number;
    create_at: number;
    update_at: number;

    status: ReminderStatus;
    timezone: string;
    recurrence?: Recurrence;
    channel_id?: string;
    post_id?: string;
    team_id?: string;

    /** Recomputed by the server on every read, and absent when the user can no longer open the message. */
    permalink?: string;

    /** The repeat rule as a readable phrase, built by the server so the two never disagree. */
    repeat_text?: string;
    occurrences?: number;
}

export interface PluginConfig {
    max_reminders: number;
    max_recurring: number;
    min_interval_secs: number;
    max_horizon_days: number;
    scheduler_enabled: boolean;
    max_message_length: number;
    default_hour: number;
    timezone: string;
}

export interface ParsePreview {
    ok: boolean;

    /** Present when ok is false: why the phrase could not be read. */
    message?: string;
    due_at?: number;
    repeat_text?: string;
    recurrence?: Recurrence | null;
}

/** What the modal was opened with. */
export interface ModalContext {

    /** Set when creating a reminder from a message. */
    postId?: string;
    channelId?: string;

    /** Set when editing an existing reminder. */
    reminder?: Reminder;
}
