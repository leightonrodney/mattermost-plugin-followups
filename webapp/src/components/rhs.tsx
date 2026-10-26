import React, {useCallback, useEffect, useMemo, useState, useSyncExternalStore} from 'react';

import ReminderRow from './reminder_row';

import {ApiError, client} from '../client';
import {getState, openModal, remindersChanged, subscribe} from '../store';
import type {ParsePreview, PluginConfig, Reminder} from '../types';

type Tab = 'upcoming' | 'repeating' | 'done';

const TABS: Array<{id: Tab; label: string}> = [
    {id: 'upcoming', label: 'Upcoming'},
    {id: 'repeating', label: 'Repeating'},
    {id: 'done', label: 'Done'},
];

function isFinished(reminder: Reminder): boolean {
    return reminder.status === 'delivered' || reminder.status === 'cancelled' || reminder.status === 'failed';
}

function filterFor(tab: Tab, reminders: Reminder[]): Reminder[] {
    switch (tab) {
    case 'upcoming':
        return reminders.filter((r) => !isFinished(r));
    case 'repeating':
        return reminders.filter((r) => !isFinished(r) && Boolean(r.recurrence));
    case 'done':
        return reminders.filter(isFinished);
    default:
        return reminders;
    }
}

const EMPTY_STATES: Record<Tab, string> = {
    upcoming: 'Nothing scheduled. Add a reminder above, or use /remind in any channel.',
    repeating: 'No repeating reminders yet. Try "every weekday at 9am to post the update".',
    done: 'Reminders you have received or cancelled will appear here.',
};

interface Props {
    config: PluginConfig | null;
}

export default function RemindersSidebar({config}: Props) {
    const revision = useSyncExternalStore(subscribe, () => getState().revision);

    const [reminders, setReminders] = useState<Reminder[]>([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState('');
    const [tab, setTab] = useState<Tab>('upcoming');
    const [busyId, setBusyId] = useState('');

    const [phrase, setPhrase] = useState('');
    const [preview, setPreview] = useState<ParsePreview | null>(null);
    const [adding, setAdding] = useState(false);

    const load = useCallback(async () => {
        setError('');
        try {
            setReminders(await client.list());
        } catch (caught) {
            setError(caught instanceof ApiError ? caught.message : 'Could not load your reminders.');
        } finally {
            setLoading(false);
        }
    }, []);

    useEffect(() => {
        load();
    }, [load, revision]);

    // The preview is debounced so that typing does not send a request per
    // keystroke; the server does the parsing so the sidebar and the slash
    // command can never disagree about what a phrase means.
    useEffect(() => {
        const text = phrase.trim();
        if (!text) {
            setPreview(null);
            return undefined;
        }

        let cancelled = false;
        const timer = window.setTimeout(async () => {
            try {
                const result = await client.parse(text);
                if (!cancelled) {
                    setPreview(result);
                }
            } catch {
                if (!cancelled) {
                    setPreview(null);
                }
            }
        }, 300);

        return () => {
            cancelled = true;
            window.clearTimeout(timer);
        };
    }, [phrase]);

    const runAction = useCallback(async (reminder: Reminder, action: () => Promise<unknown>) => {
        setBusyId(reminder.id);
        setError('');
        try {
            await action();
            remindersChanged();
        } catch (caught) {
            setError(caught instanceof ApiError ? caught.message : 'That did not work. Please try again.');
        } finally {
            setBusyId('');
        }
    }, []);

    const onAdd = useCallback(async (event: React.FormEvent) => {
        event.preventDefault();
        const text = phrase.trim();
        if (!text) {
            return;
        }

        setAdding(true);
        setError('');
        try {
            await client.create({text});
            setPhrase('');
            setPreview(null);
            remindersChanged();
        } catch (caught) {
            setError(caught instanceof ApiError ? caught.message : 'Could not set that reminder.');
        } finally {
            setAdding(false);
        }
    }, [phrase]);

    const visible = useMemo(() => filterFor(tab, reminders), [tab, reminders]);

    return (
        <div className='followups-rhs'>
            <form
                className='followups-quickadd'
                onSubmit={onAdd}
            >
                <label
                    className='followups-sr-only'
                    htmlFor='followups-quickadd-input'
                >
                    {'Remind me'}
                </label>
                <input
                    id='followups-quickadd-input'
                    className='followups-input'
                    value={phrase}
                    placeholder='in 30 minutes to check the build'
                    onChange={(event) => setPhrase(event.target.value)}
                />
                <button
                    type='submit'
                    className='followups-button followups-button--primary'
                    disabled={adding || !phrase.trim()}
                >
                    {adding ? 'Adding…' : 'Add'}
                </button>
            </form>

            <div
                className='followups-preview'
                aria-live='polite'
            >
                {preview?.ok && preview.due_at && (
                    <span>
                        {new Date(preview.due_at).toLocaleString(undefined, {
                            weekday: 'short',
                            day: 'numeric',
                            month: 'short',
                            hour: '2-digit',
                            minute: '2-digit',
                        })}
                        {preview.repeat_text ? ` · repeats ${preview.repeat_text}` : ''}
                    </span>
                )}
                {preview && !preview.ok && (
                    <span className='followups-preview--bad'>{preview.message}</span>
                )}
            </div>

            <div
                className='followups-tabs'
                role='tablist'
                aria-label='Reminder lists'
            >
                {TABS.map((entry) => (
                    <button
                        key={entry.id}
                        type='button'
                        role='tab'
                        id={`followups-tab-${entry.id}`}
                        aria-selected={tab === entry.id}

                        // Only the selected tab's panel is in the document, so
                        // only that tab claims to control one. Pointing the
                        // other two at ids that do not exist leaves a screen
                        // reader following a dangling reference, and axe does
                        // not flag it, so nothing but this would catch it.
                        aria-controls={tab === entry.id ? `followups-panel-${entry.id}` : undefined}
                        className={`followups-tab${tab === entry.id ? ' followups-tab--active' : ''}`}
                        onClick={() => setTab(entry.id)}
                    >
                        {entry.label}
                    </button>
                ))}
            </div>

            {config && !config.scheduler_enabled && (
                <p className='followups-banner'>
                    {'Reminder delivery is currently turned off by an administrator. You can still add and edit reminders.'}
                </p>
            )}

            {error && (
                <p
                    className='followups-error followups-error--block'
                    role='alert'
                >
                    {error}
                </p>
            )}

            <div
                id={`followups-panel-${tab}`}
                role='tabpanel'
                aria-labelledby={`followups-tab-${tab}`}
            >
                {loading && <p className='followups-empty'>{'Loading your reminders…'}</p>}

                {!loading && visible.length === 0 && (
                    <p className='followups-empty'>{EMPTY_STATES[tab]}</p>
                )}

                {!loading && visible.length > 0 && (
                    <ul className='followups-list'>
                        {visible.map((reminder) => (
                            <ReminderRow
                                key={reminder.id}
                                reminder={reminder}
                                busy={busyId === reminder.id}
                                onEdit={(target) => openModal({reminder: target})}
                                onSnooze={(target, minutes) => runAction(target, () => client.snooze(target.id, minutes))}
                                onCancel={(target) => runAction(target, () => client.cancel(target.id))}
                                onDelete={(target) => runAction(target, () => client.remove(target.id))}
                            />
                        ))}
                    </ul>
                )}
            </div>
        </div>
    );
}
