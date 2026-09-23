// Types du mini-framework de l'IDE : vues enregistrées dans des zones,
// onglets d'éditeurs et actions contextuelles de la barre d'outils.
import type { Component } from 'svelte';
import type { IconName } from './Icon.svelte';

export type Zone = 'left' | 'right' | 'bottom' | 'editor';

export type TabParams = Record<string, string>;

/** Ce qu'il faut pour ouvrir un onglet : le type d'éditeur et ses paramètres. */
export interface TabSpec {
  kind: string;
  params: TabParams;
}

export interface Tab extends TabSpec {
  /** `kind:clé` — une seule instance par objet */
  id: string;
  /** false : onglet d'aperçu, remplacé par la sélection suivante */
  pinned: boolean;
}

export interface ToolbarAction {
  id: string;
  label: string;
  icon?: IconName;
  run: () => unknown;
  disabled?: boolean;
  primary?: boolean;
  danger?: boolean;
  title?: string;
  /** raccourci affiché dans l'infobulle */
  shortcut?: string;
}

/** Détails affichés par le panneau « Propriétés ». */
export interface Properties {
  title: string;
  subtitle?: string;
  rows: [string, string][];
}

interface BaseView {
  id: string;
  title: string;
  icon: IconName;
  order?: number;
}

/** Vue d'une zone latérale ou basse (sans paramètres). */
export interface PanelView extends BaseView {
  zone: 'left' | 'right' | 'bottom';
  component: Component;
  /** pastille (compteur) affichée sur l'icône ou l'onglet */
  badge?: () => string | number | undefined;
}

/** Type d'éditeur : un composant qui reçoit l'onglet, plus des accroches. */
export interface EditorView extends BaseView {
  zone: 'editor';
  component: Component<{ tab: Tab }>;
  /** clé unique de l'objet édité (l'identifiant d'onglet est `kind:clé`) */
  key: (params: TabParams) => string;
  tabTitle: (tab: Tab) => string;
  tabIcon?: (tab: Tab) => IconName;
  /** infobulle de l'onglet */
  tooltip?: (tab: Tab) => string;
  dirty?: (tab: Tab) => boolean;
  /**
   * Regroupe les onglets qui partagent un même état (brouillon d'une
   * méthodologie) : les modifications ne sont abandonnées qu'à la fermeture du
   * dernier onglet du groupe.
   */
  group?: (tab: Tab) => string;
  /** le groupe a-t-il des modifications (fermeture du dernier onglet du groupe) ? */
  groupDirty?: (tab: Tab) => boolean;
  /** abandonne les modifications du groupe (fermeture sans enregistrer) */
  discard?: (tab: Tab) => void;
  properties?: (tab: Tab) => Properties | undefined;
}

export type View = PanelView | EditorView;
