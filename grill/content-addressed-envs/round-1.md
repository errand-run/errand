# Environnements adressés par leur contenu : round 1

> Laisse **Réponse** vide pour accepter la recommandation. `?` demande plus de détails.
> Écris n'importe quoi d'autre (réponse, objection, commentaire) sous une question, puis Envoyer.

Les faits établis : micromamba 2.10 sait `create --clone`, `env export --explicit` (URL + md5 de chaque paquet conda, mais rien de pip),
et `create -f` lit un fichier explicite ou un lockfile conda-lock. Un prefix conda ne se déplace pas ( shebangs, chemins écrits à
l'installation ). Le `Ssh` a déjà `cache` : le répertoire d'errand sur la machine ( `~/.cache/errand` par défaut, `$WORK` à Jean Zay ).

---

## Q1 : Un seul principe pour tous les layers qui construisent

Plutôt qu'un ADR « Micromamba », un principe commun : **ce qu'un layer construit est nommé par ce qui le définit, rangé dans les caches
d'errand, et jamais modifié une fois construit**. Un changement de déclaration fait un *autre* artefact ; l'ancien devient une entrée de
cache qui part quand plus rien ne s'en sert. S'appliquent : Micromamba/Conda, Uv, Venv `create=True`, Apptainer et Docker/Podman avec
`recipe`, le disque Windows. Ne s'appliquent pas : Nix et Guix ( déjà adressés par contenu ), Module, Vars, Ssh, Slurm…

Micromamba est implémenté en premier ; les autres suivent dans le même ADR, layer par layer.

➡️ **Recommandé :** oui, un principe commun dans un ADR, Micromamba d'abord.

**Réponse :**

---

## Q2 : Ce qui est à l'utilisateur, ce qui est à errand

Deux cas tranchés, comme `Venv( python = … )` sans `create` le fait déjà :

- **`Micromamba( name = "x" )` sans rien d'autre** : un env de l'utilisateur. errand l'utilise ( `run -n x` ), ne le construit pas, ne le
  met pas à jour, n'en garde pas de record. Pareil pour `Apptainer( image = … )` sans `recipe`, `Docker( image = … )` sans `recipe`.
- **Avec ce qui le définit** ( `packages`, `python`, `pip`, `requirements` ; une `recipe` ) : à errand, adressé par contenu.

Conséquence : l'« adoption » de l'ADR 0004 disparaît pour ces layers ( un artefact à errand est toujours construit par errand ; un
artefact externe n'est jamais jugé ). Ce que devient `name` quand il est donné *avec* des paquets est une question du round suivant.

➡️ **Recommandé :** oui, ces deux cas, et plus d'adoption.

**Réponse :**

---

## Q3 : L'identité, la déclaration ou un lock

Une déclaration `numpy` sans version donne un autre env dans un mois, ou sur une autre machine. Trois options :

- **(a) Hash de la déclaration seulement.** Simple. « Même déclaration » ne veut pas dire « mêmes paquets » : lmo et Jean Zay peuvent
  diverger, et un env reconstruit après un nettoyage n'est plus le même.
- **(b) Hash de la déclaration, et le lock constaté gardé dans le record.** On sait ce qu'on a eu, on ne peut pas le refaire ailleurs.
- **(c) Un lock versionné dans le projet** ( à la pixi / `uv.lock` ) : `errand-envs.lock`, par env et par plateforme
  ( `linux-64`, `osx-arm64` ). L'identité de l'env est le hash de son lock. La déclaration ne sert qu'à savoir quand résoudre à nouveau :
  son hash est noté dans le lock, et une déclaration changée re-résout. Sur une machine qui a déjà le lock de sa plateforme
  ( Jean Zay, sans internet pour le solveur mais avec un miroir ou `build_on` ), on installe sans résoudre : mêmes paquets, au md5 près.
  Une plateforme sans lock le produit à sa première construction. `errand setup --update` re-résout volontairement.
  Le lock est la partie conda ( `--explicit`, URL + md5 ) et la partie pip ( versions exactes, et hashes quand l'index les donne ).

Le coût de (c) : un fichier de plus dans le projet, écrit par errand ( à committer ), et un conflit git possible quand deux machines
résolvent la même plateforme.

➡️ **Recommandé :** (c), le lock versionné dans le projet est l'identité ; la déclaration dit seulement quand résoudre.

**Réponse :**

Oui pour (c).

---

## Q4 : Où vivent les envs

- **(a) Un store par machine** ( en fait par système de fichiers ), partagé entre projets : `<cache du Ssh>/envs/mamba/<hash>`, avec le
  cache de paquets ( `pkgs_dirs` ) **sur le même système de fichiers**, sinon chaque env est une copie complète au lieu de hardlinks.
  Deux projets au même lock partagent l'env. Un env part quand aucun projet enregistré ( le registre de l'ADR 0022 ) ne le déclare
  plus et qu'il n'a pas servi depuis N jours.
- **(b) Par projet**, `<outputs>/caches/envs/<env>/<hash>` comme les build directories de l'ADR 0039 : aucun partage, ménage plus simple
  ( suit le projet ), sur le disque de scratch quand `errand_outputs_directory` en nomme un.

Avec un lock comme identité ( Q3 c ), deux projets ont rarement le même lock ; le partage gagne surtout les paquets ( hardlinks ), que
(b) a aussi si `pkgs_dirs` est sur le même système de fichiers que les outputs.

➡️ **Recommandé :** (a), un store par machine dans le `cache` d'errand, `pkgs_dirs` à côté.

**Réponse :**

---

## Q5 : Les installs éditables ( `pip install -e ../sdot` )

Leur contenu change tout le temps : ils ne peuvent pas entrer dans l'identité, et les installer dans un env immuable ( partagé, si Q4 a )
le salirait. Options :

- **(a) Un overlay par projet et par env** : un petit venv `--system-site-packages` au-dessus de l'env conda, dans
  `<outputs>/caches/build/<env>/develop`, où errand fait `pip install -e --no-deps` des chemins déclarés ( `develop = [ "../sdot" ]` ).
  L'env reste pur ; l'overlay est jetable et refait en secondes. Les dépendances de `sdot` sont à déclarer dans l'env ( ou lues de son
  `pyproject.toml` et ajoutées à la déclaration : à décider ensuite ).
- **(b) Dans l'env lui-même, hors hash** : simple, mais l'env n'est plus ce que son nom dit, et un autre projet qui le partage voit le
  `sdot` de celui-ci.
- **(c) Un `.pth` / `PYTHONPATH` posé au lancement, sans installation** : rien à construire, mais un paquet qui a une étape de build
  ( meson, scikit-build ) ne marche pas.

➡️ **Recommandé :** (a), un overlay venv par projet, `develop = [ … ]` installé `--no-deps`.

**Réponse :**

(a) a l'air bien mais comment faire pour les dépendances du projet en lien ? Est-ce qu'on peut se débrouiller par exemple pour que les dépendances de sdot soient bien dans le micromamba et pas dans le .venv ?

---
