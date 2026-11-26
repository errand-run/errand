# sdot : ce qui change pour les services de la visionneuse ( errand ADR 0041 )

errand a changé la façon dont une page obtient ses services. Le nouveau errand n'est pas encore installé : `make install` dans `~/Projects/errand`. L'ancien mécanisme est retiré, sans rétrocompatibilité :

- plus de `GET .errand/services`, `.errand/svc/NOM/`, `POST .errand/services/NOM`, `.errand/machines/M`, `.errand/machines/M/paths` ni `.errand/copy` ;
- errand ne lit plus le `services.json` du store, ni `errand-services.json` par le point d'entrée `errand.services` ;
- errand ne calcule plus de version sur les fichiers.

## Ce qu'il faut faire

1. **La description, en entier, envoyée par la page.** Pour chaque service (blender, sdot.ao, sdot.region), la page envoie une description portable par `POST .errand/srv`, en JSON :

   ```json
   { "on": "<id d'une machine>", "name": "blender", "protocol": 1, "text": "…",
     "run": [ "{tool:blender}", "-b", "--factory-startup", "-P", "{file:blender.py}", "--", "--port", "{port}" ],
     "source": { "git": "https://github.com/sdot-team/sdot", "rev": "<commit>", "dir": "src/sdot/viz/services" },
     "tools": { "blender": { "min": "5.0", "look": { … }, "version_from": [ … ], "fetch": { … } } },
     "needs": { "gpus": 1 }, "idle": "10m" }
   ```

   - Clés acceptées : `name`, `protocol`, `text`, `run`, `vars`, `source`, `tools`, `needs` ( `gpus`, `gpu_ram`, `ram`, `cpus` ), `busy`, `ready`, `idle`, `grace`, `version_of`. Toute autre clé est refusée (`gpu_kind`, `platforms`, `optional` n'en font plus partie).
   - `source` : `git`, puis `rev` (un commit) ou `dev: true`, puis `dir`. S'y ajoutent `package` (où est le paquet Python dans le dépôt, pour `{python}` : `pip install git+URL@REV#subdirectory=PACKAGE`), ou bien `pip` (`sdot==2026.11.3`) avec `index`.
   - Avec un checkout modifié (aujourd'hui `dirty`), la description dit `source: { git, dev: true, dir }` et `version_of: [ "blender.py", … ]`. errand prend alors le checkout déclaré par `errand serve dev REPO PATH --env ENV`.
   - `declare()` peut continuer d'écrire ces descriptions dans le `services.json` du store (la page les y lit), ou la page peut les porter elle-même : errand ne fait pas la différence. Il n'y a plus de version calculée ; `version()` côté sdot n'a plus de raison d'être.

2. **L'appel et la réponse.**
   - `GET .errand/machines` donne `[ { id, name, here } ]` pour le menu des machines. La page garde son choix (par exemple dans `localStorage`), puisqu'errand ne le garde plus.
   - `POST .errand/srv` répond, en cas de succès, `200 { url: ".errand/srv/<MACHINE>/<NAME>~<HASH>/", state, key, on }`. Les requêtes du service passent par cette `url` (n'importe quelle méthode, un corps, une WebSocket), avec `X-Errand-Root` ajouté comme avant.
   - En cas de refus, il répond `403 { error: "not authorized" | "no store" | "unavailable", why, html, authorize }`.

3. **Le panneau réduit.** En cas de refus, le panneau insère `html` tel quel. C'est un fragment en lecture seule, avec des classes `errand-ok`, `errand-missing` et `errand-setup`, et un lien « Set it up on M » qui ouvre la fenêtre d'autorisation d'errand dans un nouvel onglet. Le panneau ajoute un bouton **Retry**, qui refait le `POST`, sans sondage. Le menu des machines, le diagnostic, les lignes à copier, les champs de chemin et « Copier vers M » disparaissent : la fenêtre d'errand fait tout cela.

4. **Sans errand**, rien ne change : `.errand/srv` renvoie 404, la page marche sur ses fichiers et accepte `?svc.NOM=URL`.

Exemple minimal complet : `~/Projects/errand/docs/guide/serve.md`, section *A page that asks for a service*.
