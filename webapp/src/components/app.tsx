import React, {useEffect, useState, useSyncExternalStore} from 'react';

import ReminderModal from './reminder_modal';
import RemindersSidebar from './rhs';

import {client} from '../client';
import {getState, subscribe} from '../store';
import type {PluginConfig} from '../types';

/**
 * The administrator's limits change rarely, so they are fetched once and shared
 * by every mounted component rather than refetched per render.
 */
let cachedConfig: PluginConfig | null = null;
let inFlight: Promise<PluginConfig> | null = null;

function useConfig(): PluginConfig | null {
    const [config, setConfig] = useState<PluginConfig | null>(cachedConfig);

    useEffect(() => {
        if (cachedConfig) {
            return undefined;
        }

        let cancelled = false;
        inFlight ??= client.getConfig();

        inFlight.
            then((loaded) => {
                cachedConfig = loaded;
                if (!cancelled) {
                    setConfig(loaded);
                }
            }).
            catch(() => {
                // The components fall back to their own defaults, so a failure
                // here degrades the hints rather than breaking the interface.
                inFlight = null;
            });

        return () => {
            cancelled = true;
        };
    }, []);

    return config;
}

/** Resets the cached configuration. Exported for tests. */
export function resetConfigCache() {
    cachedConfig = null;
    inFlight = null;
}

/**
 * Mounted once by registerRootComponent. It renders nothing until something
 * asks for the dialog, which keeps it out of the way the rest of the time.
 */
export function ModalRoot() {
    const modal = useSyncExternalStore(subscribe, () => getState().modal);
    const config = useConfig();

    if (!modal) {
        return null;
    }

    return (
        <ReminderModal
            key={modal.reminder?.id ?? modal.postId ?? 'new'}
            context={modal}
            config={config}
        />
    );
}

/** Mounted by registerRightHandSidebarComponent. */
export function SidebarRoot() {
    const config = useConfig();
    return <RemindersSidebar config={config}/>;
}
