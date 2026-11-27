# Environnements adressés par leur contenu : round 3

> Laisse **Réponse** vide pour accepter la recommandation. `?` demande plus de détails.
> Écris n'importe quoi d'autre (réponse, objection, commentaire) sous une question, puis Envoyer.

Réglé jusqu'ici : [decisions.md](decisions.md).

---

## Q1 : « Tout ce qui a de l'influence » sur la résolution

**Ce que dit micromamba.** Le solveur ne voit de la machine que ses **paquets virtuels** : `__unix` / `__win`, `__linux` ( version du
noyau ), `__osx`, `__glibc`, `__cuda` ( la version que le pilote supporte ), `__archspec` ( la micro-architecture : `x86_64_v3`, `zen2`,
`arm64` ). Tout le reste est dans le lock ( l'état des canaux, la version du solveur ). C'est donc la liste complète, côté conda.

**Ce qu'un dry-run dit.** `micromamba create --dry-run --json` donne, pour chaque paquet choisi, ses `depends` et `constrains` : on y lit
les contraintes de la solution sur les paquets virtuels ( vérifié ici : `numpy` → `__osx >=11.0` ). C'est l'influence **positive**.
L'influence **négative** n'y est pas : sur une machine sans GPU, le solveur prend jax ou torch CPU, et *aucun* paquet de la solution ne
mentionne `__cuda`. Le dry-run seul ne suffit donc pas.

**La règle proposée.** Un lock garde, pour chaque résolution :

1. **ce qui existait** au moment de résoudre : la plateforme, et **la présence de `__cuda`** ( c'est le seul paquet virtuel dont
   l'absence change les builds choisis ; les autres n'agissent que par des contraintes `>=` ) ;
2. **les contraintes** de ses paquets sur les paquets virtuels, lues dans la solution ( `__glibc >=2.28`, `__cuda >=12`,
   `__archspec` pour les builds par niveau de micro-architecture ).

Une machine réutilise une résolution existante quand elle a **la même présence** et **satisfait toutes ses contraintes** ; sinon elle
résout et ajoute la sienne au lock. lmo ( CUDA 12.4 ) et Jean Zay ( CUDA 12.2 ) partagent ainsi un lock tant qu'aucun paquet ne demande
`__cuda >=12.4`. Un env peut forcer la présence ( `cuda = False` : CPU même sur une machine à GPU ).

**Côté pip**, la liste figée garde les hashes de **toutes** les roues de chaque version : chaque machine prend celle qui lui va
( `manylinux_2_28` selon sa glibc ) ; s'il n'y en a aucune, c'est une résolution à part.

**Ce qui n'entre pas** ( ce n'est pas une entrée du solveur ) : la *compute capability* du GPU ( sm_70 / sm_90 ), un MPI ou des pilotes
du système, les extensions CPU au-delà d'`__archspec`. Le record les note ( `nvidia-smi`, `lscpu` ), pour comprendre un échec, sans en
faire une identité.

➡️ **Recommandé :** oui, cette règle : présence de `__cuda` + plateforme, et contraintes lues dans la solution.

**Réponse :**

---

## Q2 : Le refus devant un env modifié à la main

Ce que j'ai compris de ta réponse, à confirmer :

- errand compare l'env au lock ( `conda-meta/*.json`, `pip list` ) et **refuse de lancer**. Le message dit ce qui a changé ( ajouté,
  retiré, changé de version, côté conda et côté pip ) et **propose les lignes de déclaration** qui en feraient l'état voulu
  ( `packages = [ …, "scipy" ]`, `pip = [ … ]` ). errand n'édite pas `errand-envs.py` lui-même : c'est du Python, on ne le réécrit pas.
- Deux issues : la déclaration est changée → nouvelle résolution → **nouvel env construit à côté** ; ou `errand setup --force` → l'env
  est reconstruit **depuis le lock**, dans un nouveau répertoire ( `<hash>-2` ), l'ancien part avec le ménage.
- `--no-setup` lance quand même dans l'env modifié, comme il lance aujourd'hui dans une image dont la construction a échoué.

➡️ **Recommandé :** oui, comme décrit.

**Réponse :**

---

## Q3 : Le micromamba d'errand

Aujourd'hui le layer prend le `micromamba` du `PATH` ( ou `executable` ), ce qui oblige à déclarer `executable` et `root_prefix` derrière
Slurm. Avec un store à errand :

- errand **va chercher et épingle son propre micromamba** ( dans `<cache>/micromamba/<version>`, comme il le fait déjà pour uv ), avec
  son propre `pkgs_dirs` dans le store. Rien à déclarer sur un nœud de calcul ; le root prefix de l'utilisateur n'est jamais lu.
  `executable` reste pour un site qui impose le sien.
- **`Conda` ne sert plus qu'aux envs externes** ( `Conda( name = "x" )` ) : un env à errand se construit toujours avec le micromamba
  d'errand, qu'on ait écrit `Micromamba` ou `Conda`.

➡️ **Recommandé :** oui, micromamba épinglé par errand, `Conda` pour les envs externes seulement.

**Réponse :**

---

## Q4 : Les envs qui existent déjà

`~/.mamba/envs/prj-jax`, `loom-jax`, etc. : déclarés aujourd'hui avec des paquets, ils deviendront des envs à errand, résolus et
construits dans le store à la première commande ( le premier lock s'écrit alors ). Les anciens restent où ils sont, **à toi** : errand ne
les touche pas et ne les supprime pas ; il ne cherche pas non plus à les reprendre ( pas de compatibilité, comme pour le portage en Go ).
`errand envs` dit une fois qu'un env portant ce `name` existe dans ton root prefix et n'est plus utilisé.

➡️ **Recommandé :** oui, aucune reprise, les anciens laissés à toi, signalés une fois.

**Réponse :**

---

## Q5 : Quand un env du store part

- Un env **déclaré** par un projet enregistré ( son lock, sur cette machine ) ne part jamais tout seul.
- Un env qu'aucun projet ne déclare plus part après **30 jours** sans usage ( un retour en arrière sur une branche le retrouve d'ici là ).
- `errand caches` le montre et le retire à la main, comme le reste ( ADR 0022 ) ; le cache de paquets micromamba est nettoyé avec
  ( `clean --all` des paquets qu'aucun env restant ne lie ).
- Sur une machine **partagée** ( ADR 0028 §7 ), 7 jours au lieu de 30.

➡️ **Recommandé :** oui, jamais tant qu'il est déclaré, 30 jours ( 7 sur machine partagée ) sinon.

**Réponse :**

---
