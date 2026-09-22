# GOAP — interface web

Interface Svelte 5 + Vite + TypeScript pour la plateforme GOAP : lancement et
suivi des processus, revue des changements, consultation des référentiels et
des méthodologies.

## Démarrage

Prérequis : Node.js ≥ 20.19 (ou ≥ 22.12) et une passerelle GOAP accessible.

```sh
cd web
npm install
npm run dev          # http://localhost:5173
```

En développement, le serveur Vite relaie toutes les requêtes `/goap.*`
(RPC Connect en JSON) vers la passerelle, par défaut `http://localhost:8080` :

```sh
GOAP_GATEWAY_URL=http://passerelle:8080 npm run dev
```

Si la passerelle exige une authentification, cliquez sur « Configurer un
jeton » en bas de la barre latérale : le jeton est stocké dans `localStorage`
(clé `goap.token`) et envoyé en en-tête `Authorization: Bearer …`.

## Scripts

| Commande          | Rôle                                            |
| ----------------- | ----------------------------------------------- |
| `npm run dev`     | serveur de développement avec proxy             |
| `npm run build`   | build de production dans `dist/`                |
| `npm run check`   | vérification des types (`svelte-check`)         |
| `npm run preview` | sert le build localement                        |

En production, servez `dist/` derrière le même domaine que la passerelle (les
URL sont relatives), ou définissez `VITE_GOAP_BASE_URL` au moment du build.

## Organisation

- `src/lib/api.ts` : types des messages et client `rpc()` (Connect/JSON, sans génération de code)
- `src/lib/nav.svelte.ts` : navigation par fragment d'URL (`#processus/<id>`, `#changement/<id>`…)
- `src/lib/items.ts` : mise en forme des items d'un changement
- `src/lib/components/` : écrans et composants
