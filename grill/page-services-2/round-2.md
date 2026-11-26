# Services des pages après l'ADR 0040 : tour 2

> Laisse **Réponse** vide pour accepter la recommandation. `?` demande plus de détails.
> Écris autre chose (réponse, objection, commentaire) sous n'importe quelle question, puis Envoyer (ou enregistre et dis « ok » dans le chat).

Réglé au tour 1 : [decisions.md](decisions.md). La page envoie une description portable et reçoit `.errand/srv/NOM~HASH/` ou un refus. Il n'y a ni sondage ni bail, et autoriser se fait dans une page d'errand.

---

## Q1 : autoriser dans une page d'errand, ouverte comme un paiement par carte

Tu demandais si le jeton suffit à protéger cette page. Oui, à trois conditions :

- **Elle est servie par la page d'errand** (`http://127.0.0.1:20501/…`), pas par la vue (`…:20503/<jeton>/…`). Ce sont deux origines différentes : le JS de la page cliente ne peut ni lire cette page ni cliquer dedans.
- **Il faut le cookie d'errand** (posé par `errand web`, `HttpOnly`, `SameSite=Strict`). Un autre compte de la machine qui trouve l'URL n'a pas ce cookie, et le bouton ne fait rien pour lui. Ce mécanisme existe déjà pour les actions de la page d'errand, avec la vérification de l'origine.
- **Jamais dans une iframe** (`frame-ancestors 'none'`, déjà là). Sinon la page cliente pourrait la recouvrir et faire cliquer « Autoriser » à l'insu de la personne. Elle s'ouvre donc dans une nouvelle fenêtre, ou en navigation de toute la page.

Déroulé :

1. La page demande `blender` sur lmo et reçoit le refus, avec `authorize: "http://127.0.0.1:20501/authorize/<id>"`.
2. Elle affiche « Mettre en place sur lmo » et ouvre ce lien dans une nouvelle fenêtre.
3. La page d'errand montre la description, ce que lmo a et ce qui manque, la commande complète, puis un bouton **Autoriser**.
4. Une fois l'autorisation donnée, la fenêtre se ferme. La personne clique « Réessayer » dans la page cliente.

**Les refus ne sont pas stockés.** La description refusée est gardée en mémoire par la page d'errand, sous un identifiant au hasard, une heure. Elle ne sert qu'à afficher la page d'autorisation, et rien n'est écrit sur disque. Je retire donc `refused-services.json` et la liste « refused lately » ajoutés à l'étape 4, pour les pages comme pour les clés (`there --only`). Les clés gardent seulement une ligne dans le journal de l'agent.

➡️ **Recommandé :** oui à tout cela. La page s'ouvre dans une nouvelle fenêtre, la page cliente a un bouton « Réessayer », la description est gardée une heure en mémoire, et les refus ne sont plus stockés sur disque, y compris pour les clés.

**Réponse :**

---

## Q2 : le diagnostic « en HTML inline », c'est-à-dire ?

Je comprends ta demande comme ceci : errand produit lui-même le HTML du diagnostic, et la page cliente l'insère tel quel. Elle n'a plus de code de rendu à elle (aujourd'hui, environ 200 lignes dans `Visualizer.py`).

```js
const r = await errandServe( { … } );
if ( r.error ) panel.innerHTML = r.html;      // HTML statique d'errand : état, ce qui manque, lien « Mettre en place »
```

- Ce fragment est **en lecture seule** : aucun script, aucun bouton qui agit. Il contient seulement du texte, la liste de ce que M a ou n'a pas, et le lien de Q1 (`target="_blank"`). Toute action reste dans la page d'errand, car la page cliente peut modifier ce qu'elle insère.
- Il porte des classes (`errand-ok`, `errand-missing`, `errand-setup`) et un style minimal inclus, que la page peut redéfinir.
- Le JSON garde aussi les champs (`state`, `why`, `authorize`) pour une page qui préfère dessiner elle-même.

Autre lecture possible : une iframe d'errand dans la page. Elle serait plus jolie, mais elle rouvre le risque de faire cliquer à l'insu de la personne (Q1), sauf si l'iframe ne contient aucun bouton.

➡️ **Recommandé :** un fragment HTML en lecture seule dans la réponse (`html`), à insérer tel quel, avec le lien vers la page d'autorisation. Pas d'iframe.

**Réponse :**

---

## Q3 : ce que fait « Autoriser »

À partir de la description portable, sur la machine M :

1. **Résoudre les jokers** :
   - `{tool:blender}` : chercher l'outil (`look`, `PATH`, cache d'errand) et lire sa version dans des fichiers ;
   - `{file:blender.py}` : prendre le fichier dans le code au commit `source.rev` ;
   - `{python}` : prendre l'env de ce service.
2. **Montrer ce qui manque**, avec la source, la taille et le sha256 : un Blender à télécharger, le dépôt au commit, un env à construire (`pip install git+URL@REV`). Une source qui n'est pas dans `errand.sources` est en rouge, et il faut cocher « Je fais confiance à cette source » pour continuer.
3. **Montrer la commande complète**, avec les chemins de M.
4. **Un seul bouton**, qui télécharge ce qui manque, compose, puis écrit l'autorisation `to: pages` (le hash portable mène à la définition de M). La page suit l'avancement (téléchargement, construction de l'env).

En ligne de commande, la même chose : `errand serve authorize --to pages --on M --spec -`, la description sur l'entrée standard, avec le même affichage et une question oui / non.

➡️ **Recommandé :** oui. Un bouton fait télécharger, composer et autoriser, après une seule confirmation qui montre tout. La ligne de commande fait pareil avec `--spec -`.

**Réponse :**

---

## Q4 : ce qui part de l'ADR 0027

La description arrive avec la demande, et une personne autorise. Plusieurs pièces n'ont donc plus de raison d'être :

| pièce | devient |
|---|---|
| le point d'entrée `errand.services`, la lecture de `errand-services.json` dans les envs de M, `{python}` pris d'un paquet installé | **part** : la description vient de la page ; le code vient de `source`, dans l'env du service |
| `NOM#VERSION` calculé sur les fichiers (`version_of`) | **part** : le hash de la description, avec son `rev`, le remplace |
| le menu des machines diagnostiquées en parallèle (`.errand/machines/M`) | **part** : la page d'autorisation diagnostique la machine choisie, à la demande |
| `POST .errand/machines/M/paths` (un chemin par outil) | **devient** un champ de la page d'autorisation |
| `errand serve probe` (lancer `--version` une fois) | **devient** un bouton de la page d'autorisation, avec la commande exacte |
| `errand serve fetch` (URL + sha256, `git+`, `git:`), `errand.sources` | **restent**, appelés par « Autoriser » |
| `errand serve look`, `serve install` | **partent** (`install` est déjà remplacé par `authorize`) |

sdot n'aurait plus besoin du point d'entrée ni de calculer des versions. Son `errand-services.json` resterait la source de la description que `declare()` copie dans le store, sans que errand le lise.

➡️ **Recommandé :** oui, ce tableau.

**Réponse :**

---

## Q5 : le store sur une autre machine

Un service lancé sur M lit les fichiers de la vue (`X-Errand-Root`). Si M ne les voit pas (ni l'original, ni un système de fichiers partagé), l'ADR 0027 proposait « Copier vers M » dans la page cliente, puis tenait la copie à jour tant que la vue était ouverte.

Proposition : cette copie devient une ligne de plus dans la page d'autorisation (« les fichiers de la vue, 1,3 Go, copiés dans le cache d'errand sur lmo »), confirmée avec le reste. Une fois faite, elle reste tenue à jour sans rien demander, tant que la vue est ouverte. Une autre vue qui demande le même service autorisé sur M sans que M ait ses fichiers reçoit un refus « store absent », avec le même lien.

➡️ **Recommandé :** oui. La copie fait partie de « Mettre en place », et elle est confirmée dans la page d'errand.

**Réponse :**

---

## Q6 : un service en développement

Aujourd'hui, avec un checkout modifié (`dirty`), sdot écrit `NOM#dev`, et errand prend le checkout déclaré dans `dev_sources` (`~/.config/errand/config.json`, ADR 0028).

Proposition : la description porte `source: { git: …, dev: true }` au lieu d'un `rev`. Son hash ne dépend alors pas du code (qui change à chaque édition). La page d'autorisation propose les checkouts de M connus dans `dev_sources` pour ce dépôt, et l'autorisation note celui qui a été choisi. Une modification du code ne change ni l'URL ni l'autorisation. Le serveur repart sur le nouveau code (ADR 0040 : un programme qui a changé est un autre serveur derrière le même port), à condition que `version_of` désigne les fichiers à surveiller.

➡️ **Recommandé :** oui. `dev: true` à la place de `rev`, le checkout choisi à l'autorisation, et les fichiers à surveiller dans `version_of`.

**Réponse :**

---
