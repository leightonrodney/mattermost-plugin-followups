import React from 'react';

/**
 * The channel header button's icon: a clock, drawn with currentColor so that it
 * inherits whatever the active Mattermost theme sets for header icons rather
 * than hard-coding a colour that only suits one of them.
 */
export default function ClockIcon() {
    return (
        <svg
            width='20'
            height='20'
            viewBox='0 0 24 24'
            fill='none'
            stroke='currentColor'
            strokeWidth='2'
            strokeLinecap='round'
            strokeLinejoin='round'
            aria-hidden='true'
            focusable='false'
        >
            <circle
                cx='12'
                cy='12'
                r='9'
            />
            <polyline points='12 7 12 12 15.5 14'/>
        </svg>
    );
}
