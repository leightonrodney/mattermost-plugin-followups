import type {Reminder} from './types';

/**
 * Formatting helpers. Everything is rendered in the timezone the reminder was
 * created in rather than the browser's, so that a reminder set before a trip
 * still reads the way it was set.
 */

function formatter(
    timezone: string,
    options: Intl.DateTimeFormatOptions,
    locale?: string,
): Intl.DateTimeFormat {
    try {
        return new Intl.DateTimeFormat(locale, {...options, timeZone: timezone || 'UTC'});
    } catch {
        // An unknown zone name should not stop a list from rendering.
        return new Intl.DateTimeFormat(locale, options);
    }
}

/** "Tue 3 Nov, 14:30" — the form used in list rows. */
export function formatDueDate(timestamp: number, timezone: string, locale?: string): string {
    return formatter(timezone, {
        weekday: 'short',
        day: 'numeric',
        month: 'short',
        hour: '2-digit',
        minute: '2-digit',
    }, locale).format(new Date(timestamp));
}

/** The full form, including the year and zone, used in tooltips. */
export function formatDueDateLong(timestamp: number, timezone: string, locale?: string): string {
    return formatter(timezone, {
        weekday: 'long',
        day: 'numeric',
        month: 'long',
        year: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
        timeZoneName: 'short',
    }, locale).format(new Date(timestamp));
}

/**
 * "in 25 minutes", "in 3 days", "2 hours ago" — the relative phrasing that
 * tells someone at a glance how soon a reminder is.
 */
export function formatRelative(timestamp: number, now: number = Date.now()): string {
    const seconds = Math.round((timestamp - now) / 1000);
    const absolute = Math.abs(seconds);

    const units: Array<[Intl.RelativeTimeFormatUnit, number]> = [
        ['second', 60],
        ['minute', 60],
        ['hour', 24],
        ['day', 7],
        ['week', 4.35],
        ['month', 12],
        ['year', Number.POSITIVE_INFINITY],
    ];

    let value = absolute;
    let unit: Intl.RelativeTimeFormatUnit = 'second';

    for (const [candidate, limit] of units) {
        unit = candidate;
        if (value < limit) {
            break;
        }
        value /= limit;
    }

    const rounded = Math.round(value) * Math.sign(seconds || 1);

    try {
        return new Intl.RelativeTimeFormat(undefined, {numeric: 'auto'}).format(rounded, unit);
    } catch {
        return seconds >= 0 ? 'soon' : 'overdue';
    }
}

/** True when a scheduled reminder's time has passed without it being delivered. */
export function isOverdue(reminder: Reminder, now: number = Date.now()): boolean {
    return reminder.status === 'scheduled' && reminder.due_at < now;
}

/**
 * Formats a local Date as the value an <input type="datetime-local"> expects.
 * Date.toISOString would convert to UTC first and shift the displayed time.
 */
export function toDateTimeLocalValue(date: Date): string {
    const pad = (n: number) => String(n).padStart(2, '0');
    return (
        `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}` +
        `T${pad(date.getHours())}:${pad(date.getMinutes())}`
    );
}

/** Reads a datetime-local value back as a timestamp, or null when it is not a valid date. */
export function fromDateTimeLocalValue(value: string): number | null {
    if (!value) {
        return null;
    }
    const parsed = new Date(value);
    const time = parsed.getTime();
    return Number.isNaN(time) ? null : time;
}
