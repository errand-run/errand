# Services des pages après l'ADR 0040 : tour 1

> Laisse **Réponse** vide pour accepter la recommandation. `?` demande plus de détails.
> Écris autre chose (réponse, objection, commentaire) sous n'importe quelle question, puis Envoyer (ou enregistre et dis « ok » dans le chat).

**Où on en est.** Aujourd'hui (ADR 0026, 0027, 0028, plus l'étape 4 de 0040) :

- Le store porte un `services.json` (format 2) : `blender#3fa29c1e`, le protocole, une copie de la description du paquet.
- La page appelle `.errand/services`, `.errand/machines`, `.errand/machines/M`, `POST .errand/services/NOM {on}`, `POST .errand/copy`, `POST .errand/machines/M/paths`, puis `.errand/svc/NOM/…` (le relais). Le choix de machine est gardé par errand, par vue.
- La description qui *lance* vient d'un paquet installé sur M (point d'entrée `errand.services`, `errand-services.json`), jamais du store. Son code vient du dépôt, au commit du store, dans un env par service.
- Le panneau de sdot (≈ 200 lignes de `Visualizer.py`) montre un menu de machines, l'état, « ce que l'agent de M a trouvé », les lignes à taper, un champ de chemin par outil, et « Copier vers M ».
- Depuis l'étape 4, c'est une **autorisation** (`errand serve authorize --to pages NOM#VERSION`) qui mène du nom à une définition complète. Le relais refuse ce qui n'est pas autorisé.

L'ADR 0027 voulait que seuls les paquets installés sur M disent quoi lancer. La raison : *un store peut porter n'importe quelle commande*. Une autorisation donnée par une personne, la commande entière sous les yeux, couvre maintenant ce risque. La page peut donc décrire le service elle-même, et ne rien obtenir tant qu'on ne l'a pas autorisé. C'est ta proposition, et c'est ce qui permet de simplifier beaucoup.

---

## Q1 : la page envoie une description portable du service, et son hash en est l'identité

Comme pour ollama, la page dit tout du service, en données. Mais la page ne connaît pas les chemins de la machine qui sert, donc la description garde les jokers de l'ADR 0027 (un ensemble fermé), résolus sur cette machine :

```js
const blender = await errandServe( {
  name: "blender", protocol: 1, text: "Images de la vue courante par Blender",
  run: [ "{tool:blender}", "-b", "--factory-startup", "-P", "{file:blender.py}", "--", "--port", "{port}" ],
  source: { git: "https://github.com/sdot-team/sdot", rev: "807e184", dir: "src/sdot/viz/services" },
  tools: { blender: { min: "5.0", look: { … }, version_from: [ … ], fetch: { … } } },
  needs: { gpus: 1 }, idle: "10m" } );
// → { url: ".errand/srv/blender~5c1e09ab/", state: "rests" }  ou  { error: "not authorized", … }
```

- Le hash vaut 8 hexa de la description normalisée, calculé par errand : le producteur n'a plus à refaire l'algorithme de l'ADR 0027 §2.
- Un autre commit (`rev`) donne un autre hash, donc un autre service. Un checkout en cours d'édition passe par `NOM#dev` (ADR 0028).
- La description peut venir du `services.json` du store, ou être écrite dans le JS : errand ne fait pas la différence.

Autre possibilité : la page n'envoie que `NOM#VERSION` (comme aujourd'hui), et la description reste dans le paquet installé sur M.

➡️ **Recommandé :** oui. La page envoie la description portable, et le hash de cette description est l'identité. Les jokers sont résolus sur la machine qui sert, au moment d'autoriser.

**Réponse :**

---

## Q2 : l'URL rendue est `.errand/srv/NOM~HASH/`, relative à la vue

- On garde une seule origine et un seul jeton, sans CORS, avec `X-Errand-Root` ajouté par le relais (ADR 0026).
- `~` plutôt que `#` : dans une URL, `#` ouvre le fragment, que le navigateur n'envoie jamais. errand écrit déjà `LABEL~hash` pour les serveurs.
- L'URL reste la même tant que la description et l'autorisation ne changent pas. La page peut la garder pour la session.

➡️ **Recommandé :** oui, `.errand/srv/NOM~HASH/`, relative à l'adresse de la vue. `.errand/svc/NOM/` disparaît.

**Réponse :**

---

## Q3 : la machine est dite dans la demande (`on`), hors identité, et c'est la page qui garde le choix

L'ADR 0027 gardait le choix chez errand, par vue, pour qu'une page ne puisse pas diriger chaque requête où elle veut. Avec les autorisations, une page ne peut atteindre que ce qu'une personne a autorisé *sur cette machine*. Le choix n'a donc plus besoin d'être protégé.

- `errandServe( { …, on: "lmo" } )`. Sans `on`, c'est la machine qui sert la page.
- La page garde son choix (dans `localStorage`, par exemple) et le propose dans son menu. errand ne garde plus de choix par vue (`serve/choices.json` disparaît).
- Deux machines différentes, ou deux `on` différents, donnent deux URL.
- errand donne toujours la liste des machines qu'il connaît (`GET .errand/machines`), pour que le menu ait de quoi s'afficher.

➡️ **Recommandé :** oui. `on` est dans la demande, la page garde le choix, et `POST .errand/services/NOM` disparaît.

**Réponse :**

---

## Q4 : un refus répond « non autorisé », avec un lien vers la page d'errand

```json
{ "error": "not authorized", "on": "lmo", "name": "blender", "hash": "5c1e09ab",
  "setup": "http://127.0.0.1:20501/#machines/lmo/refused/5c1e09ab" }
```

- Le refus est noté (déjà fait à l'étape 4) et apparaît dans l'onglet Machines de la page d'errand, avec ce que la page a demandé.
- La page affiche la raison et un lien « Mettre en place dans errand ». Elle n'autorise jamais rien elle-même, et ne reçoit jamais de commande à exécuter.
- Une fois l'autorisation donnée dans la page d'errand, la page cliente redemande et obtient son URL. Elle peut redemander d'elle-même toutes les quelques secondes tant que le lien est ouvert, ou sur un bouton.

➡️ **Recommandé :** oui, ce format. La page redemande toutes les 3 s tant qu'elle a un refus et que l'onglet est visible.

**Réponse :**

Je ne vois pas trop l’intérêt de stocker les refus.
En fait, je préfère qu'on propose un bouton reload/retry pour les utilisateurs qu'un polling.
Enfin, est-ce qu'on pourrait gérer les autorisations depuis l'interface web ? En effet, si c'est errand qui génère la page d'autorisation (et non le html/js de la page générée), c'est un peu comme lorsqu'il y a un paiement par carte bleu. Cependant je reconnais qu'il y a le risque qu'un autre user accède à la page. Mais si le fonctionnement en token est toujours actif, c'est bon, non ?

---

## Q5 : l'aide à l'installation et le diagnostic vont dans la page d'errand, plus dans chaque page cliente

Aujourd'hui chaque page cliente (sdot) refait un panneau complet : menu de machines diagnostiquées en parallèle, « ce que l'agent a trouvé », lignes à copier, champ de chemin par outil, bouton de copie du store. Ça fait beaucoup de code dans chaque client, qui suit les changements d'errand.

Proposition : tout ça se déplace dans l'onglet Machines de la page d'errand, sur la ligne de la demande refusée.

- Ce que M a trouvé : les outils avec leur version lue dans des fichiers, le code (dépôt au commit, ou checkout `dev`), les cartes, le store.
- Ce qui manque, avec la source, la taille et le sha256.
- La commande complète qui sera lancée.
- Un bouton « Autoriser » qui confirme le tout.

La page cliente ne garde que trois choses : son menu de machines (Q3), l'état de son service, et le lien de Q4.

➡️ **Recommandé :** oui. Diagnostic et aide à l'installation dans la page d'errand seulement. Les appels `.errand/machines/M`, `.errand/machines/M/paths` et `.errand/copy` quittent l'interface des pages.

**Réponse :**

Le principe est bon, mais est-ce qu'on pourrait l'avoir en html inline ?

---

## Q6 : pas de bail pour une page ; l'URL vaut tant que l'autorisation tient

Un programme qui demande un port reçoit un bail (`keep`, `again_in`), parce que le port est sur sa machine et doit rester écouté. Pour une page, l'« écoute », c'est le relais de la page d'errand, qui tourne de toute façon.

- L'URL marche tant que l'autorisation existe. `idle` et `grace` viennent de l'autorisation, et le relais compte les connexions comme un usage.
- Si l'autorisation est retirée, l'URL répond 403 « not authorized », et la page redemande (Q4).
- Un serveur arrêté (inactif, ou qui a cédé sa place) est redémarré par la requête suivante, comme pour un port.

➡️ **Recommandé :** oui, pas de bail pour une page. L'autorisation en tient lieu.

**Réponse :**

---

## Q7 : sans errand, rien ne change

Ouverte par un serveur statique quelconque, la page reçoit un 404 sur la demande. Elle marche alors avec ses fichiers seuls, grise ce qui dépend du service, et accepte `?svc.NOM=URL` vers un serveur lancé à la main (`--root DIR`, CORS pour 127.0.0.1), comme dans l'ADR 0026.

➡️ **Recommandé :** oui, on garde ça tel quel.

**Réponse :**

---
