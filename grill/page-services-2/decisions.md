# Services des pages après l'ADR 0040 : décisions

## Tour 1 (2026-11-26)

- **Ce que la page envoie** : une description portable du service, en données (`run` avec les jokers `{tool:}`, `{file:}`, `{python}`, `{port}`, `source` dépôt + commit, `tools`, `needs`, `idle`, `protocol`, `text`). Son hash, calculé par errand, en est l'identité. Les jokers sont résolus sur la machine qui sert, au moment d'autoriser.
- **URL** : `.errand/srv/NOM~HASH/`, relative à la vue (une origine, un jeton, `X-Errand-Root`). `.errand/svc/NOM/` disparaît.
- **Machine** : `on` dans la demande, hors identité. La page garde son choix, errand ne garde plus de choix par vue. `GET .errand/machines` reste pour le menu.
- **Refus** : « not authorized ». Pas de sondage : un bouton « Réessayer » dans la page. Autoriser se fait dans une page générée par errand (comme un paiement par carte), pas dans le HTML/JS de la page cliente. Hugo ne voit pas l'intérêt de stocker les refus (à préciser au tour 2).
- **Aide et diagnostic** : dans errand, plus dans chaque client. Hugo voudrait les avoir en HTML inline (à préciser au tour 2).
- **Bail** : pas de bail pour une page. L'URL vaut tant que l'autorisation tient, et `idle` / `grace` viennent de l'autorisation.
- **Sans errand** : inchangé (404, fichiers seuls, `?svc.NOM=URL`, `--root`).

## Tour 2 (2026-11-26)

Toutes les recommandations acceptées.

- **Autoriser** : dans une page servie par errand (origine `127.0.0.1:20501`, cookie `HttpOnly` / `SameSite=Strict`, jamais en iframe), ouverte dans une nouvelle fenêtre comme un paiement par carte ; la page cliente a un bouton « Réessayer ».
- **Refus** : jamais stockés sur disque. La description refusée est gardée une heure en mémoire, sous un identifiant au hasard, pour la page d'autorisation. `refused-services.json` et « refused lately » partent, y compris pour les clés (qui gardent une ligne de journal).
- **Diagnostic inline** : un fragment HTML en lecture seule dans la réponse (`html`), sans script ni action, avec des classes et un style minimal, et le lien vers la page d'autorisation. Pas d'iframe. Le JSON garde `state`, `why`, `authorize`.
- **Autoriser fait** : résoudre les jokers sur M, montrer ce qui manque (source, taille, sha256 ; une source inconnue en rouge, à cocher), montrer la commande complète, puis un bouton qui télécharge, compose et écrit l'autorisation `to: pages`. En ligne de commande : `errand serve authorize --to pages --on M --spec -`.
- **Ce qui part de l'ADR 0027** : le point d'entrée `errand.services` et la lecture de `errand-services.json`, `NOM#VERSION` calculé sur les fichiers, le menu des machines diagnostiquées, `serve look` et `serve install`. Les chemins par outil et `probe` vont dans la page d'autorisation ; `fetch` et `errand.sources` restent.
- **Store ailleurs** : la copie vers M est une ligne de la page d'autorisation, confirmée avec le reste, puis tenue à jour tant que la vue est ouverte. Un store absent donne un refus « store absent » avec le même lien.
- **Développement** : `source: { git, dev: true }` à la place de `rev`. Le checkout de M est choisi à l'autorisation parmi `dev_sources`, et les fichiers à surveiller sont dans `version_of`.

## Tour 3 (2026-11-26)

- **Machine dans l'URL** : `.errand/srv/<MACHINE>/<NOM>~<HASH>/`, où MACHINE est un identifiant unique (8 hexa de `system.MachineID`), jamais un nom ambigu. `GET .errand/machines` donne ces identifiants avec les noms, pour le menu de la page. HASH est celui de la description, donc celui de l'autorisation.
- **Appel** : un seul `POST .errand/srv` documenté (description et `on` en JSON), réponse `{ url, state }` ou `{ error, why, html, authorize }`. Pas de bibliothèque JS à importer.
- **Suite** : l'ADR 0041 (amende 0026, 0027, 0028, 0040) d'abord, relue par Hugo ; puis errand, sans rétrocompatibilité ; puis sdot, par sa propre session.

## Tour 4 (2026-11-26)

- **Autoriser sans stockage** : le fragment porte un formulaire POST `target=_blank` vers `/authorize` de la page d'errand ( description, `on`, la vue, en champs cachés ) ; errand vérifie l'origine des vues, son cookie et la vue, résout et montre ; Authorize renvoie la description elle-même ( origine et cookie d'errand ). `pending` disparaît. `POST .errand/srv` donne `authorize` sans id.
- **Toujours un lien** sauf pour une description illisible ; la fenêtre sait déclarer un checkout pour `source.dev` ( chemin, env, la commande montrée, un clic ).
- **Ordre des agents** : un tampon de build ( `system.Built` ), l'ordre `Release` puis version ( `versionBefore` ) puis `Built` ; seul ce qui est avant est retiré ; un binaire plus ancien utilise l'agent en place.
- **Builds sans tampon** : toujours un tampon ( `Makefile`, `packaging/backend.py`, les builds des tests ). Les tests tournent déjà dans des répertoires et sockets à eux ( `ERRAND_CACHE_DIR`, `XDG_RUNTIME_DIR`, `XDG_CONFIG_HOME` d'un dossier temporaire, nettoyé à la fin ) : rien de plus à ajouter.

## Tour 5 (2026-11-26)

Toutes les recommandations acceptées.

- **Fichiers et env séparés** : `files` ( un dépôt, à un commit ou un checkout, pour `{file:}` ; rien d'installé ) et `python: { install: [ exigences pip | { git, rev | dev, package, version_of } ], index }` pour `{python}`. `source` disparaît.
- **Un seul `pip install`** avec toutes les entrées : un dépôt donné ( `-e CHECKOUT` ou `git+URL@REV` ) fournit son paquet, pip ne va pas le chercher sur l'index ; un conflit de versions est le message de pip, montré dans la fenêtre.
- **Commit ou checkout par dépôt**, choisi par la page pour toute la page, dans l'identité ; `version_of` dans chaque entrée, relatif au dépôt, avec des motifs ( `**` ) ; la fenêtre déclare chaque checkout manquant.
- **Révisé (Hugo, après essai)** : le choix d'une version, d'un commit ou d'un checkout se fait **par service**, pas pour toute la page : chaque service a son env. La description de chaque service le dit déjà ; c'est le panneau de la page qui change.

## Tour 6 (2026-11-26)

- **Instances** : `ERRAND_INSTANCE=NOM` donne à tout ce qui est d'errand le nom de l'instance ; `ERRAND_CACHE_DIR` reste, et contient un sous-dossier par instance ( Hugo ) ; celle en usage s'appelle `main`. L'ancien rangement se déplace dans `main/` de lui-même ( un renommage ), la première fois qu'aucun agent ni page n'en tourne ; Pas de lien hors des instances ( Hugo ) : le nom stable est `<errand>/main/bin/errand` ; la clé de Boucles sur lmo est à changer quand lmo a déplacé son errand dans main/.
- **La file de la machine reste commune** ; les tests gardent la leur.
- **L'instance suit errand** sur les autres machines ( son répertoire, son agent, son nom stable là-bas ).
- **Les projets ne changent pas** ; `result.json` dit l'instance.
- **La voir et nettoyer** : une ligne sur un terminal ( `errand [try]: …` ), l'en-tête de la page ; `errand instances`, `errand instances remove NOM` ( ici et là où elle est allée ).
