import React from 'react';

import {formatDueDate, formatDueDateLong, formatRelative, isOverdue} from '../time';
import type {Reminder} from '../types';

interface Props {
    reminder: Reminder;
    busy: boolean;
    onEdit: (reminder: Reminder) => void;
    onSnooze: (reminder: Reminder, minutes: number) => void;
    onCancel: (reminder: Reminder) => void;
    onDelete: (reminder: Reminder) => void;
}

const STATUS_LABELS: Record<Reminder['status'], string> = {
    scheduled: 'Scheduled',
    delivering: 'Sending',
    delivered: 'Delivered',
    cancelled: 'Cancelled',
    failed: 'Failed',
};

export default function ReminderRow({reminder, busy, onEdit, onSnooze, onCancel, onDelete}: Props) {
    const finished = reminder.status === 'delivered' || reminder.status === 'cancelled' || reminder.status === 'failed';
    const overdue = isOverdue(reminder);

    return (
        <li className={`followups-row${overdue ? ' followups-row--overdue' : ''}`}>
            <div className='followups-row__main'>
                <p className='followups-row__message'>{reminder.message}</p>

                <p className='followups-row__meta'>
                    <time
                        dateTime={new Date(reminder.due_at).toISOString()}
                        title={formatDueDateLong(reminder.due_at, reminder.timezone)}
                    >
                        {formatDueDate(reminder.due_at, reminder.timezone)}
                    </time>
                    {!finished && (
                        <span className='followups-row__relative'>
                            {overdue ? ' · overdue' : ` · ${formatRelative(reminder.due_at)}`}
                        </span>
                    )}
                    {finished && (
                        <span className='followups-row__relative'>{` · ${STATUS_LABELS[reminder.status]}`}</span>
                    )}
                </p>

                {reminder.repeat_text && (
                    <p className='followups-row__repeat'>{`Repeats ${reminder.repeat_text}`}</p>
                )}

                {reminder.permalink && (
                    <p className='followups-row__link'>
                        <a
                            href={reminder.permalink}
                            rel='noreferrer'
                        >
                            {'Open the message'}
                        </a>
                    </p>
                )}
            </div>

            <div className='followups-row__actions'>
                {!finished && (
                    <>
                        <button
                            type='button'
                            className='followups-button followups-button--small'
                            disabled={busy}
                            onClick={() => onSnooze(reminder, 60)}
                        >
                            {'Snooze 1h'}
                        </button>
                        <button
                            type='button'
                            className='followups-button followups-button--small'
                            disabled={busy}
                            onClick={() => onEdit(reminder)}
                        >
                            {'Edit'}
                        </button>
                        <button
                            type='button'
                            className='followups-button followups-button--small followups-button--danger'
                            disabled={busy}
                            onClick={() => onCancel(reminder)}
                        >
                            {'Cancel'}
                        </button>
                    </>
                )}
                {finished && (
                    <button
                        type='button'
                        className='followups-button followups-button--small followups-button--danger'
                        disabled={busy}
                        onClick={() => onDelete(reminder)}
                    >
                        {'Delete'}
                    </button>
                )}
            </div>
        </li>
    );
}
