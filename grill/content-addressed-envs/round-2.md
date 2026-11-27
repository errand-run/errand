# Environnements adressés par leur contenu : round 2

> Laisse **Réponse** vide pour accepter la recommandation. `?` demande plus de détails.
> Écris n'importe quoi d'autre (réponse, objection, commentaire) sous une question, puis Envoyer.

Réglé au round 1 ( [decisions.md](decisions.md) ) : principe commun, externe / à errand, **lock versionné comme identité**, store par
machine, éditables dans un overlay venv.

---

## Q1 : Les dépendances des paquets liés vont dans l'env micromamba

Oui, c'est faisable, et c'est la conséquence naturelle du lock. errand lit les dépendances de chaque chemin de `develop` et les **ajoute
à la déclaration de l'env** avant de résoudre ; elles entrent donc dans le lock, et dans l'env immuable. L'overlay ne reçoit que les
paquets liés eux-mêmes, en `-e --no-deps`.

- **Ce qui est lu** : `[project].dependencies` du `pyproject.toml`, les extras demandés ( `develop = [ "../sdot[viz]" ]` ), et
  `[build-system].requires` : un éditable meson-python ou scikit-build se reconstruit à l'import, avec les outils de build de l'env.
  Quand les dépendances sont `dynamic`, errand demande les métadonnées au backend de build ( `pip install --dry-run --report` ).
- **Les paquets liés entre eux** ( `sdot` dépend de `loom`, et `../loom` est aussi dans `develop` ) : leur nom est retiré des dépendances
  ajoutées, sinon l'env contiendrait un `loom` de PyPI sous le `loom` éditable.
- **conda ou pip** : on résout d'abord la partie conda déclarée, puis les dépendances ajoutées passent par pip ( ou uv ) dans l'env ;
  pip saute celles que conda fournit déjà ( un `numpy` conda a son `dist-info` ). Pour qu'une dépendance vienne de conda-forge plutôt
  que de PyPI, on la déclare dans `packages` : c'est la seule chose à écrire. Traduire automatiquement les noms PyPI en noms conda
  ( la table de prefix.dev ) est possible plus tard, pas d'emblée.
- **Conséquence** : un changement des dépendances de `sdot` change la déclaration, donc re-résout, donc un nouvel env. Modifier son
  code ne change rien ( c'est l'overlay ).

➡️ **Recommandé :** oui, dépendances ( et `build-system.requires` ) des paquets liés lues et ajoutées à la déclaration, en pip par-dessus
la partie conda, les paquets liés entre eux exclus.

**Réponse :**

---

## Q2 : Utiliser un env à la main

Un env à errand est dans le store, sous un hash, et l'overlay des éditables est par projet : `micromamba activate prj-jax` ne donnerait
de toute façon pas les éditables. Proposition :

- **`name` devient une étiquette facultative** : ce qu'affiche `errand envs`, rien d'autre. errand n'écrit rien dans le root prefix de
  l'utilisateur ( pas de lien `~/.mamba/envs/<name>` : un `micromamba env remove -n name` à travers le lien viderait l'env partagé ).
- **À la main** : `errand shell --env X` ( existe déjà, avec l'overlay ) ; `errand python --env X` imprime le chemin de l'interpréteur à
  utiliser ( celui de l'overlay quand il y en a un, sinon celui de l'env ), pour VS Code ou un `$( errand python )` dans un script.
- Ton CLAUDE.md global ( « un env micromamba nommé d'après le projet », `micromamba run -n` ) est réécrit pour passer par errand.

➡️ **Recommandé :** oui, `name` simple étiquette, `errand shell` et `errand python --env X`, rien dans ton root prefix.

**Réponse :**

---

## Q3 : Un env à errand modifié à la main

Quelqu'un fait `pip install truc` dans un env du store ( partagé entre projets ). Options :

- **(a) Détecté, et remplacé** : le stamp ( `conda-meta/history`, `site-packages` ) change sans qu'errand ait construit ; le record dit
  `modifié à la main`, errand le signale, et la prochaine préparation reconstruit l'env **dans un nouveau répertoire**
  ( `<hash>-2` ), sans toucher à celui que des runs utilisent peut-être encore ; l'ancien part avec le ménage des caches.
- **(b) Lecture seule** ( `chmod -R a-w` ) : un `pip install` échoue tout de suite. Mais les fichiers sont des hardlinks du cache de
  paquets : le `chmod` les rend aussi en lecture seule là-bas, et des paquets qui écrivent dans leur propre répertoire à l'exécution
  ( caches de numba, `__pycache__` ) cassent ou ralentissent.
- **(c) Signalé seulement**, utilisé tel quel.

L'overlay des éditables, lui, est par projet et jetable : modifié à la main, il est refait.

➡️ **Recommandé :** (a), détecté, signalé, reconstruit à côté.

**Réponse :**

(a), détecté, signalé avec refus de continuer et proposition d'ajouter les paquets modifiés enlevés dans la déclaration de la couche.

---

## Q4 : Où et pour quoi on résout

Un lock « par plateforme » ne suffit pas : lmo et Jean Zay sont tous deux `linux-64`, mais n'ont ni le même pilote CUDA ni la même glibc,
et le solveur choisit des builds de jax ou torch d'après ces **paquets virtuels** ( `__cuda`, `__glibc` ). Un lock résolu sur lmo peut
ne pas tourner à Jean Zay.

- **(a) On résout sur la machine qui construit**, avec ses vrais paquets virtuels ; le lock est rangé sous
  `plateforme + __cuda ( majeur.mineur ) + __glibc`. Deux machines identiques sur ces points partagent un lock ; sinon chacune a le sien.
  Une machine sans accès au solveur ( pas d'internet ) prend un lock résolu ailleurs pour sa clé, ou passe par `build_on`.
- **(b) On résout ici, pour toutes les plateformes**, comme pixi, avec des paquets virtuels **déclarés** dans l'env
  ( `system = { cuda = "12.2", glibc = "2.28" }` ) : un seul endroit résout, mais il faut déclarer ce que les machines ont, et la partie
  pip se résout mal pour une autre plateforme.

➡️ **Recommandé :** (a), résolu sur la machine qui construit, lock rangé par plateforme et paquets virtuels.

**Réponse :**

Oui. Idéalement, il faudrait qu'on soit capable sur la machine de dire "tout ce qui a de l'influence" (GPU ? Autre élément système ou matériel spécifique ?). Est-ce que "tout ce qui a de l'influence" peut être déterminé en lisant les infos d'install en dry ?

---

## Q5 : Le fichier de lock

- **Un seul fichier** `errand-envs.lock` à la racine, à côté d'`errand-envs.py`, en yaml lisible : par env, par clé ( Q4 ), le hash de la
  déclaration, la liste explicite conda ( URL + md5 ) et la liste pip ( versions exactes, avec hashes quand l'index les donne ).
- La partie pip passe par **uv** ( `uv pip install --python <env>` ), qu'errand sait déjà aller chercher : plus rapide que pip, et il sait
  installer exactement une liste figée ( `--no-deps`, `--require-hashes` ).
- Un lock ne garde que les clés vivantes : une clé dont aucune machine n'a reconstruit depuis longtemps reste ( le fichier est petit ),
  `errand setup --update` les refait toutes.

➡️ **Recommandé :** oui, un `errand-envs.lock` unique, partie pip par uv.

**Réponse :**

---

## Q6 : Les images et uv

Une image ne se locke pas : un `.def` fait `apt-get install` et `From: ubuntu:24.04` bouge. Pour les images, l'identité reste donc
**le hash de la déclaration**, mais complet :

- Apptainer : le `.def`, **les fichiers que `%files` y copie** ( aujourd'hui ignorés : en changer un ne reconstruit rien ), `build_flags`,
  `pip`. Docker / Podman : le Dockerfile **et le contexte** qu'il copie ( ce que `.dockerignore` laisse ).
- `image` devient facultatif : par défaut `<cache>/images/<hash>.sif`, ou le tag `errand/<projet>:<hash>`. Un `image` donné avec une
  `recipe` reste un alias ( lien vers la dernière construite ). `install` / `run_from` ( IDRIS ) ne changent pas.
- Le record garde ce qui a été obtenu ( digest de la base, `pip freeze` dans l'image ) : on sait ce qu'on a eu, à défaut de le refaire.
- **Uv** : quand le projet a un `uv.lock`, c'est lui l'identité ( `uv sync --frozen` dans `<store>/uv/<hash>` ) ; sinon même traitement
  que Micromamba, la partie conda en moins.

➡️ **Recommandé :** oui, hash de déclaration complète pour les images ( `%files` et contexte compris ), `uv.lock` comme identité pour Uv.

**Réponse :**

---
