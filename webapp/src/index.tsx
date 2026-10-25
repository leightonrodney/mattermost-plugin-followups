import React from 'react';
import type {Store} from 'redux';

import type {GlobalState} from '@mattermost/types/store';

import {ModalRoot, SidebarRoot} from './components/app';
import ClockIcon from './components/icon';
import manifest from './manifest';
import {openModal} from './store';
import type {PluginRegistry} from './types/mattermost-webapp';

import './styles.scss';

export default class Plugin {
    public async initialize(registry: PluginRegistry, store: Store<GlobalState>) {
        // The dialog lives in a root component so that it can be opened from
        // the post menu and from the sidebar without either owning it.
        registry.registerRootComponent(ModalRoot);

        const {toggleRHSPlugin} = registry.registerRightHandSidebarComponent(SidebarRoot, 'Follow-ups');

        registry.registerChannelHeaderButtonAction(
            <ClockIcon/>,
            () => store.dispatch(toggleRHSPlugin),
            'Follow-ups',
            'Your reminders',
        );

        // "Remind me" on any message. The server checks that the user may still
        // read the channel before it stores the link, and again before it puts
        // a permalink in the delivered reminder.
        registry.registerPostDropdownMenuAction(
            'Remind me',
            (postId: string) => openModal({postId}),
        );
    }
}

declare global {
    interface Window {
        registerPlugin(pluginId: string, plugin: Plugin): void;
    }
}

window.registerPlugin(manifest.id, new Plugin());
