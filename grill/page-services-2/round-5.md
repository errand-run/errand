# Fichiers et env Python, plusieurs dépôts : tour 5

> Laisse **Réponse** vide pour accepter la recommandation. `?` demande plus de détails.
> Écris autre chose (réponse, objection, commentaire) sous n'importe quelle question, puis Envoyer.

**Le problème** (loom). En développement, l'env de `{python}` est construit par `pip install -e <checkout de sdot>`. Cette commande va chercher `loom-ffi` sur PyPI, ce qui cache ton loom local. Aujourd'hui, une description n'a qu'une seule `source`, qui sert à la fois aux fichiers (`{file:}`, Blender) et à l'env (`{python}`). Ta remarque est juste : ce sont deux besoins différents.

---

## Q1 : séparer les fichiers du service et ce que son env installe

`source` disparaît (sans rétrocompatibilité), au profit de deux clés :

```json
"files":  { "git": "https://github.com/sdot-team/sdot", "rev": "807e184", "dir": "src/sdot/viz/services" },
"python": { "install": [
              { "git": "https://github.com/sdot-team/sdot", "dev": true },
              { "git": "https://github.com/hleclerc/loom", "dev": true, "package": "python" },
              "numpy>=2" ],
            "index": "https://pypi.org/simple" }
```

- **`files`**, pour `{file:REL}` : un dépôt, soit à un commit (`rev`), soit pris dans un checkout à toi (`dev`). On n'y prend que des fichiers, rien n'est installé. C'est le cas de Blender, qui lance `blender.py` avec son propre Python.
- **`python.install`**, pour `{python}` : ce que l'env du service installe, dans l'ordre. Chaque entrée est soit une exigence pip (`"sdot==2026.11.3"`, `"numpy>=2"`, prise sur l'index), soit un dépôt : à un commit, il devient `git+URL@REV#subdirectory=PACKAGE` ; dans un checkout, `-e CHECKOUT/PACKAGE`.
- Un service qui n'a besoin que de l'un des deux ne dit que celui-là.

➡️ **Recommandé :** oui. `files` pour les fichiers, `python.install` pour l'env, et `source` disparaît.

**Réponse :**

---

## Q2 : tout en un seul `pip install`, pour qu'un dépôt donné remplace la version de l'index

errand fait **un seul appel** : `pip install -e ~/Projects/sdot-ffi -e ~/Projects/loom/python numpy>=2`. pip voit alors que `loom-ffi` est déjà fourni par ton checkout, et ne va plus le chercher sur PyPI. Il n'y a ni « remplacement » à décrire, ni ordre à respecter.

- Si ton loom ne satisfait pas ce que sdot exige (`loom-ffi>=X`), pip refuse. La fenêtre affiche alors son message.
- L'env du service (`svc-envs/NOM@HASH`) est construit une fois. Il ne l'est de nouveau que si la liste change, c'est-à-dire si le hash change.

➡️ **Recommandé :** oui, un seul appel à pip avec toutes les entrées.

**Réponse :**

---

## Q3 : commit ou checkout, par dépôt, et ce qui relance le serveur

- **La page choisit, dépôt par dépôt**, entre le commit et ton checkout (« sdot : commit / checkout », « loom : commit / checkout »). Ce choix vaut pour toute la page, et fait partie de la description, donc de l'identité : on autorise une fois « sdot en checkout + loom en checkout ».
- **En développement, le hash ne dépend pas du code.** Les fichiers à surveiller sont nommés dans chaque entrée, relativement à ce dépôt : `{ git, dev: true, version_of: [ "src/sdot/viz/**/*.py" ] }`. Une modification de l'un d'eux donne un autre serveur derrière la même adresse, qui repart au prochain accès.
- **La fenêtre** propose de déclarer chaque checkout manquant (`errand serve dev github.com/hleclerc/loom <chemin>`), comme pour sdot.
- Sans `version_of`, une modification ne relance rien : le serveur continue sur ce qu'il a chargé. Pour des modules Python, c'est le cas dès qu'ils sont importés.

➡️ **Recommandé :** oui. Un choix par dépôt pour la page ; `version_of` dans chaque entrée, avec des motifs (`**`) ; et une déclaration par checkout dans la fenêtre.

**Réponse :**

---
