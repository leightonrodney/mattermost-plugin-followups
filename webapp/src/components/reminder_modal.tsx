import React, {useCallback, useEffect, useMemo, useRef, useState} from 'react';

import {ApiError, client} from '../client';
import {closeModal, remindersChanged} from '../store';
import {fromDateTimeLocalValue, toDateTimeLocalValue} from '../time';
import type {ModalContext, PluginConfig, Recurrence} from '../types';

/** The repeat choices offered in the form, in the order they are listed. */
type RepeatKind = 'none' | 'daily' | 'weekdays' | 'weekly' | 'monthly' | 'custom';

type CustomUnit = 'minutes' | 'hours' | 'days' | 'weeks';

const REPEAT_OPTIONS: Array<{value: RepeatKind; label: string}> = [
    {value: 'none', label: 'Does not repeat'},
    {value: 'daily', label: 'Every day'},
    {value: 'weekdays', label: 'Every weekday (Mon to Fri)'},
    {value: 'weekly', label: 'Every week'},
    {value: 'monthly', label: 'Every month'},
    {value: 'custom', label: 'Custom interval…'},
];

const CUSTOM_UNITS: Array<{value: CustomUnit; label: string}> = [
    {value: 'minutes', label: 'minutes'},
    {value: 'hours', label: 'hours'},
    {value: 'days', label: 'days'},
    {value: 'weeks', label: 'weeks'},
];

interface Preset {
    label: string;
    at: (now: Date, morningHour: number) => Date;
}

const PRESETS: Preset[] = [
    {label: 'In 20 minutes', at: (now) => new Date(now.getTime() + (20 * 60 * 1000))},
    {label: 'In 1 hour', at: (now) => new Date(now.getTime() + (60 * 60 * 1000))},
    {label: 'In 3 hours', at: (now) => new Date(now.getTime() + (3 * 60 * 60 * 1000))},
    {
        label: 'Tomorrow morning',
        at: (now, morningHour) => {
            const date = new Date(now);
            date.setDate(date.getDate() + 1);
            date.setHours(morningHour, 0, 0, 0);
            return date;
        },
    },
    {
        label: 'Next Monday',
        at: (now, morningHour) => {
            const date = new Date(now);

            // Monday is 1. A remainder of zero means today is Monday, in which
            // case "next Monday" is a week away rather than this morning.
            const daysAhead = ((8 - date.getDay()) % 7) || 7;
            date.setDate(date.getDate() + daysAhead);
            date.setHours(morningHour, 0, 0, 0);
            return date;
        },
    },
];

/** Builds the rule the server expects from the form's choices and the chosen date. */
export function buildRecurrence(
    kind: RepeatKind,
    when: Date,
    customEvery: number,
    customUnit: CustomUnit,
): Recurrence | null {
    const hour = when.getHours();
    const minute = when.getMinutes();

    switch (kind) {
    case 'none':
        return null;
    case 'daily':
        return {type: 'daily', interval: 1, hour, minute};
    case 'weekdays':
        return {type: 'weekly', interval: 1, weekdays: [1, 2, 3, 4, 5], hour, minute};
    case 'weekly':
        return {type: 'weekly', interval: 1, weekdays: [when.getDay()], hour, minute};
    case 'monthly':
        return {type: 'monthly', interval: 1, day_of_month: when.getDate(), hour, minute};
    case 'custom': {
        const every = Math.max(1, Math.floor(customEvery));
        switch (customUnit) {
        case 'minutes':
            return {type: 'interval', every_seconds: every * 60, hour, minute};
        case 'hours':
            return {type: 'interval', every_seconds: every * 3600, hour, minute};
        case 'days':
            return {type: 'daily', interval: every, hour, minute};
        case 'weeks':
            return {type: 'weekly', interval: every, weekdays: [when.getDay()], hour, minute};
        default:
            return null;
        }
    }
    default:
        return null;
    }
}

/** Recovers the form's choices from an existing reminder being edited. */
function repeatKindOf(recurrence?: Recurrence): {kind: RepeatKind; every: number; unit: CustomUnit} {
    const fallback = {kind: 'none' as RepeatKind, every: 2, unit: 'hours' as CustomUnit};
    if (!recurrence) {
        return fallback;
    }

    switch (recurrence.type) {
    case 'daily':
        return recurrence.interval === 1 ?
            {...fallback, kind: 'daily'} :
            {kind: 'custom', every: recurrence.interval ?? 2, unit: 'days'};
    case 'weekly': {
        const days = recurrence.weekdays ?? [];
        const isWeekdays = days.length === 5 && [1, 2, 3, 4, 5].every((d) => days.includes(d));
        if (isWeekdays) {
            return {...fallback, kind: 'weekdays'};
        }
        return recurrence.interval === 1 ?
            {...fallback, kind: 'weekly'} :
            {kind: 'custom', every: recurrence.interval ?? 2, unit: 'weeks'};
    }
    case 'monthly':
        return {...fallback, kind: 'monthly'};
    case 'interval': {
        const seconds = recurrence.every_seconds ?? 3600;
        return seconds % 3600 === 0 ?
            {kind: 'custom', every: seconds / 3600, unit: 'hours'} :
            {kind: 'custom', every: Math.max(1, Math.round(seconds / 60)), unit: 'minutes'};
    }
    default:
        return fallback;
    }
}

interface Props {
    context: ModalContext;
    config: PluginConfig | null;
}

export default function ReminderModal({context, config}: Props) {
    const editing = context.reminder;
    const morningHour = config?.default_hour ?? 9;
    const maxLength = config?.max_message_length ?? 500;

    const initialDate = useMemo(() => {
        if (editing) {
            return new Date(editing.due_at);
        }
        const date = new Date();
        date.setMinutes(date.getMinutes() + 60, 0, 0);
        return date;
    }, [editing]);

    const initialRepeat = useMemo(() => repeatKindOf(editing?.recurrence), [editing]);

    const [message, setMessage] = useState(editing?.message ?? (context.postId ? 'Follow up on this message' : ''));
    const [when, setWhen] = useState(toDateTimeLocalValue(initialDate));
    const [repeat, setRepeat] = useState<RepeatKind>(initialRepeat.kind);
    const [customEvery, setCustomEvery] = useState(initialRepeat.every);
    const [customUnit, setCustomUnit] = useState<CustomUnit>(initialRepeat.unit);
    const [error, setError] = useState('');
    const [saving, setSaving] = useState(false);

    const dialogRef = useRef<HTMLDivElement>(null);
    const messageRef = useRef<HTMLTextAreaElement>(null);

    useEffect(() => {
        messageRef.current?.focus();
    }, []);

    // Escape closes the dialog, which is what every other Mattermost dialog does.
    useEffect(() => {
        const onKeyDown = (event: KeyboardEvent) => {
            if (event.key === 'Escape') {
                event.stopPropagation();
                closeModal();
            }
        };
        document.addEventListener('keydown', onKeyDown, true);
        return () => document.removeEventListener('keydown', onKeyDown, true);
    }, []);

    const applyPreset = useCallback((preset: Preset) => {
        setWhen(toDateTimeLocalValue(preset.at(new Date(), morningHour)));
        setError('');
    }, [morningHour]);

    const summary = useMemo(() => {
        const timestamp = fromDateTimeLocalValue(when);
        if (timestamp === null) {
            return '';
        }
        const date = new Date(timestamp);
        const recurrence = buildRecurrence(repeat, date, customEvery, customUnit);
        const label = REPEAT_OPTIONS.find((option) => option.value === repeat)?.label ?? '';
        const formatted = date.toLocaleString(undefined, {
            weekday: 'long',
            day: 'numeric',
            month: 'long',
            hour: '2-digit',
            minute: '2-digit',
        });
        return recurrence ? `${formatted} · ${label.replace('…', '').toLowerCase()}` : formatted;
    }, [when, repeat, customEvery, customUnit]);

    const save = useCallback(async (timestamp: number, trimmed: string, recurrence: Recurrence | null) => {
        setSaving(true);
        try {
            if (editing) {
                await client.update(editing.id, {message: trimmed});
                await client.update(editing.id, {
                    due_at: timestamp,
                    recurrence,
                    clear_repeat: recurrence === null,
                });
            } else {
                await client.create({
                    message: trimmed,
                    due_at: timestamp,
                    recurrence,
                    channel_id: context.channelId,
                    post_id: context.postId,
                });
            }
            remindersChanged();
            closeModal();
        } catch (caught) {
            setError(caught instanceof ApiError ? caught.message : 'Something went wrong. Please try again.');
            setSaving(false);
        }
    }, [editing, context.channelId, context.postId]);

    // Validation stays synchronous so that a mistake is reported in the same
    // tick as the click. Only the request itself is deferred.
    const onSubmit = useCallback((event: React.FormEvent) => {
        event.preventDefault();
        setError('');

        const trimmed = message.trim();
        if (!trimmed) {
            setError('Give the reminder something to say.');
            messageRef.current?.focus();
            return;
        }

        const timestamp = fromDateTimeLocalValue(when);
        if (timestamp === null) {
            setError('Choose a date and time.');
            return;
        }
        if (timestamp <= Date.now()) {
            setError('Choose a time in the future.');
            return;
        }

        // save reports its own failures through the error region, so there is
        // nothing for the caller to handle here.
        save(timestamp, trimmed, buildRecurrence(repeat, new Date(timestamp), customEvery, customUnit));
    }, [message, when, repeat, customEvery, customUnit, save]);

    let submitLabel = editing ? 'Save changes' : 'Set reminder';
    if (saving) {
        submitLabel = 'Saving…';
    }

    // A click on the backdrop closes, but a click that started inside the
    // dialog must not, or dragging to select text would dismiss it.
    const onBackdropMouseDown = useCallback((event: React.MouseEvent) => {
        if (event.target === event.currentTarget) {
            closeModal();
        }
    }, []);

    return (
        <div
            className='followups-backdrop'
            onMouseDown={onBackdropMouseDown}
        >
            <div
                className='followups-dialog'
                role='dialog'
                aria-modal='true'
                aria-labelledby='followups-dialog-title'
                ref={dialogRef}
            >
                <form onSubmit={onSubmit}>
                    <div className='followups-dialog__header'>
                        <h2 id='followups-dialog-title'>{editing ? 'Edit reminder' : 'Set a reminder'}</h2>
                        <button
                            type='button'
                            className='followups-icon-button'
                            aria-label='Close'
                            onClick={closeModal}
                        >
                            {'×'}
                        </button>
                    </div>

                    <div className='followups-dialog__body'>
                        <label
                            className='followups-label'
                            htmlFor='followups-message'
                        >
                            {'Remind me to'}
                        </label>
                        <textarea
                            id='followups-message'
                            ref={messageRef}
                            className='followups-input followups-textarea'
                            value={message}
                            maxLength={maxLength}
                            rows={2}
                            placeholder='check the deployment'
                            onChange={(event) => setMessage(event.target.value)}
                        />
                        <div className='followups-hint'>
                            {`${message.length} / ${maxLength}`}
                        </div>

                        {context.postId && (
                            <p className='followups-hint followups-hint--block'>
                                {'This reminder will link back to the message you opened it from. The message text itself is not copied.'}
                            </p>
                        )}

                        <fieldset className='followups-fieldset'>
                            <legend className='followups-label'>{'When'}</legend>
                            <div className='followups-presets'>
                                {PRESETS.map((preset) => (
                                    <button
                                        key={preset.label}
                                        type='button'
                                        className='followups-chip'
                                        onClick={() => applyPreset(preset)}
                                    >
                                        {preset.label}
                                    </button>
                                ))}
                            </div>

                            <label
                                className='followups-label followups-label--sub'
                                htmlFor='followups-when'
                            >
                                {'Date and time'}
                            </label>
                            <input
                                id='followups-when'
                                type='datetime-local'
                                className='followups-input'
                                value={when}
                                onChange={(event) => setWhen(event.target.value)}
                            />
                        </fieldset>

                        <label
                            className='followups-label'
                            htmlFor='followups-repeat'
                        >
                            {'Repeat'}
                        </label>
                        <select
                            id='followups-repeat'
                            className='followups-input'
                            value={repeat}
                            onChange={(event) => setRepeat(event.target.value as RepeatKind)}
                        >
                            {REPEAT_OPTIONS.map((option) => (
                                <option
                                    key={option.value}
                                    value={option.value}
                                >
                                    {option.label}
                                </option>
                            ))}
                        </select>

                        {repeat === 'custom' && (
                            <div className='followups-custom-interval'>
                                <label
                                    className='followups-label followups-label--sub'
                                    htmlFor='followups-every'
                                >
                                    {'Every'}
                                </label>
                                <input
                                    id='followups-every'
                                    type='number'
                                    min={1}
                                    className='followups-input followups-input--narrow'
                                    value={customEvery}
                                    onChange={(event) => setCustomEvery(Number(event.target.value))}
                                />
                                <label
                                    className='followups-sr-only'
                                    htmlFor='followups-unit'
                                >
                                    {'Interval unit'}
                                </label>
                                <select
                                    id='followups-unit'
                                    className='followups-input followups-input--narrow'
                                    value={customUnit}
                                    onChange={(event) => setCustomUnit(event.target.value as CustomUnit)}
                                >
                                    {CUSTOM_UNITS.map((unit) => (
                                        <option
                                            key={unit.value}
                                            value={unit.value}
                                        >
                                            {unit.label}
                                        </option>
                                    ))}
                                </select>
                            </div>
                        )}

                        {summary && (
                            <p className='followups-summary'>{summary}</p>
                        )}

                        <div
                            className='followups-error'
                            role='alert'
                            aria-live='assertive'
                        >
                            {error}
                        </div>
                    </div>

                    <div className='followups-dialog__footer'>
                        <button
                            type='button'
                            className='followups-button followups-button--plain'
                            onClick={closeModal}
                        >
                            {'Cancel'}
                        </button>
                        <button
                            type='submit'
                            className='followups-button followups-button--primary'
                            disabled={saving}
                        >
                            {submitLabel}
                        </button>
                    </div>
                </form>
            </div>
        </div>
    );
}
