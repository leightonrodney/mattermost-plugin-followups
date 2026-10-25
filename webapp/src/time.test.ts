import {
    formatDueDate,
    formatRelative,
    fromDateTimeLocalValue,
    isOverdue,
    toDateTimeLocalValue,
} from './time';
import type {Reminder} from './types';

function reminderAt(due: number, status: Reminder['status'] = 'scheduled'): Reminder {
    return {
        id: 'abc',
        message: 'check the build',
        due_at: due,
        create_at: due,
        update_at: due,
        status,
        timezone: 'UTC',
    };
}

describe('formatDueDate', () => {
    test('renders in the reminder\'s own timezone, not the browser\'s', () => {
        // 2026-06-01T12:00:00Z is 21:00 in Tokyo and 08:00 in New York.
        const timestamp = Date.UTC(2026, 5, 1, 12, 0, 0);

        // en-GB is pinned so the assertion does not depend on the locale of
        // the machine running the suite; the app itself passes no locale and
        // follows the viewer's.
        expect(formatDueDate(timestamp, 'Asia/Tokyo', 'en-GB')).toContain('21:00');
        expect(formatDueDate(timestamp, 'America/New_York', 'en-GB')).toContain('08:00');
    });

    test('falls back rather than throwing on an unknown zone', () => {
        const timestamp = Date.UTC(2026, 5, 1, 12, 0, 0);
        expect(() => formatDueDate(timestamp, 'Mars/Olympus_Mons', 'en-GB')).not.toThrow();
        expect(formatDueDate(timestamp, 'Mars/Olympus_Mons', 'en-GB')).not.toEqual('');
    });

    test('treats an empty zone as UTC', () => {
        const timestamp = Date.UTC(2026, 5, 1, 12, 0, 0);
        expect(formatDueDate(timestamp, '', 'en-GB')).toContain('12:00');
    });
});

describe('formatRelative', () => {
    const now = Date.UTC(2026, 5, 1, 12, 0, 0);

    test.each([
        ['25 minutes ahead', now + (25 * 60 * 1000), 'minute'],
        ['3 hours ahead', now + (3 * 60 * 60 * 1000), 'hour'],
        ['2 days ahead', now + (2 * 24 * 60 * 60 * 1000), 'day'],
        ['in the past', now - (90 * 60 * 1000), 'hour'],
    ])('describes a time %s', (_label, timestamp, unit) => {
        expect(formatRelative(timestamp, now)).toContain(unit);
    });

    test('puts past times in the past', () => {
        expect(formatRelative(now - (2 * 60 * 60 * 1000), now)).toMatch(/ago/);
    });
});

describe('isOverdue', () => {
    const now = Date.UTC(2026, 5, 1, 12, 0, 0);

    test('a scheduled reminder whose time has passed is overdue', () => {
        expect(isOverdue(reminderAt(now - 1000), now)).toBe(true);
    });

    test('a scheduled reminder still ahead is not', () => {
        expect(isOverdue(reminderAt(now + 1000), now)).toBe(false);
    });

    test('a delivered reminder is never overdue, however old', () => {
        expect(isOverdue(reminderAt(now - 1_000_000, 'delivered'), now)).toBe(false);
    });

    test('a cancelled reminder is never overdue', () => {
        expect(isOverdue(reminderAt(now - 1_000_000, 'cancelled'), now)).toBe(false);
    });
});

describe('datetime-local values', () => {
    test('round-trips a local time without shifting it to UTC', () => {
        const date = new Date(2026, 10, 3, 14, 30);
        const value = toDateTimeLocalValue(date);

        expect(value).toEqual('2026-11-03T14:30');
        expect(fromDateTimeLocalValue(value)).toEqual(date.getTime());
    });

    test('pads single-digit parts', () => {
        expect(toDateTimeLocalValue(new Date(2026, 0, 5, 9, 7))).toEqual('2026-01-05T09:07');
    });

    test('rejects an empty or unparseable value', () => {
        expect(fromDateTimeLocalValue('')).toBeNull();
        expect(fromDateTimeLocalValue('not a date')).toBeNull();
    });
});
