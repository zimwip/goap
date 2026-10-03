import { mount } from 'svelte';
import './app.css';
import App from './App.svelte';
import { forgetLegacyState } from './lib/shell/storage';

forgetLegacyState();

const target = document.getElementById('app');
if (!target) throw new Error('#app element not found');

export default mount(App, { target });
