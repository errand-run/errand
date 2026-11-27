# ADR 0043 repris : round 2

> Laisse **Réponse** vide pour accepter la recommandation. `?` demande plus de détails.
> Écris n'importe quoi d'autre (réponse, objection, commentaire) sous une question, puis Envoyer.

Round 1 : ce qui te gêne, c'est le TLS cassé de bout en bout et le HTTP en clair sur 127.0.0.1 ; la confiance doit venir des hashes du
lock ( [decisions.md](decisions.md) ).

**Faits vérifiés ici** ( micromamba 2.10, uv 0.11.6, réseau coupé ; détails dans les notes de session ) :

- **micromamba installe hors ligne** depuis une liste explicite ( `url#sha256:…` ou `url#md5` ) dont les archives sont déjà dans
  `pkgs_dirs` : `create --offline -f lock.txt`, 0 octet téléchargé. Il **revérifie le hash** d'une archive trouvée dans le cache ; sans
  hash dans la liste, une archive corrompue passe. `file://` dans la liste marche aussi.
- **uv installe hors ligne** depuis un répertoire de roues : `uv pip install --offline --no-index -f <dir> --require-hashes -r req.txt`.
  Une roue substituée est refusée, même si une bonne copie est dans son cache ; sans hash, elle passe.
- **Les agents** parlent entre machines à travers le stdio de ssh ( chiffré ), et sur une machine par une socket unix ( ou TCP
  127.0.0.1 avec jeton sous Windows ).
- **Un trou** : un paquet **déjà extrait** dans `pkgs_dirs` puis modifié est installé tel quel, avec un simple avertissement par défaut
  ( `safety_checks: warn` ) ; une modification de même taille passe sans un mot, sauf avec `--safety-checks enabled
  --extra-safety-checks`.
- micromamba ne vérifie aucune signature de dépôt : TLS, puis les hashes, c'est tout ce qu'il y a.

---

## Q1 : Plus de proxy : l'outil installe hors ligne, depuis des fichiers qu'errand apporte

Avec un lock ( ADR 0044 ), l'installation n'a plus besoin du réseau : on sait exactement quels fichiers, à quelle URL, avec quel hash.

- errand **télécharge lui-même** chaque fichier du lock, en **HTTPS vérifié par errand** jusqu'au serveur d'origine, sur la machine qui a
  internet. Le TLS est de bout en bout entre cette machine et conda-forge ou PyPI.
- Sans internet, la demande remonte par une **requête qui attend** ( ADR 0035 ) ; le fichier redescend par le transport des agents
  ( ssh entre machines, socket unix sur la machine ) : **rien ne passe en HTTP, ni en clair, nulle part**, pas même sur 127.0.0.1.
- Le fichier est vérifié par son hash à l'arrivée, rangé dans le cache, puis posé dans `pkgs_dirs` ( conda ) ou un répertoire de roues
  ( pip ). L'outil installe **hors ligne** ( `micromamba create --offline -f`, `uv pip install --offline --no-index --require-hashes` ),
  et revérifie lui-même chaque hash.
- Un relais compromis peut faire **échouer** une installation, jamais lui **substituer** un fichier.

➡️ **Recommandé :** oui, plus de proxy HTTP ; errand apporte les fichiers du lock, l'outil installe hors ligne.

**Réponse :**

Un gros oui

---

## Q2 : Le même chemin quand la machine a internet

Sur une machine connectée, l'outil pourrait télécharger lui-même ( son propre TLS ). Proposition : **toujours le même chemin pour
installer**. errand télécharge les fichiers du lock ( directement, puisqu'il a internet ), l'outil installe hors ligne. Un seul chemin à
tester, un seul cache ( non précieux, par machine, comme décidé pour 0043 ), les relais en profitent. L'outil garde son réseau pour
**résoudre** ( Q3 ), pas pour installer.

Ce qui ne marche pas alors : un canal privé avec jeton, qu'errand devrait savoir authentifier. Rien de tel chez toi aujourd'hui ; ce sera un
champ du layer le jour où il le faudra.

➡️ **Recommandé :** oui, installer passe toujours par errand, connecté ou non.

**Réponse :**

---

## Q3 : Résoudre sur une machine sans internet

Résoudre demande les index ( `repodata.json`, les pages de PyPI ), pas seulement des fichiers connus d'avance. Trois options :

- **(a) Résoudre là où il y a internet, pour la machine qui le demande.** errand relève ses paquets virtuels sur place ( `__glibc`,
  `__cuda`, `__archspec`… ), et le premier agent qui a internet résout **avec ces valeurs** ( `--platform linux-64`, `CONDA_OVERRIDE_CUDA`,
  `CONDA_OVERRIDE_GLIBC` ; `uv pip compile --python-platform … --python-version …`, vérifié hors ligne ici ). Le résultat est celui qu'aurait
  donné la machine elle-même : seules ses entrées comptent ( ADR 0044 ), et elles sont les siennes. Le lock reçoit la résolution sous la
  clé de cette machine, puis Q1 apporte les fichiers.
- **(b) Apporter les index et résoudre sur place** ( canal `file://`, index pip `file://` complété au fil des manques, l'idée de l'ADR
  0035 ) : le solveur tourne bien chez elle, mais il faut monter des centaines de Mo d'index, et pip recommence à chaque page manquante.
- **(c) Ne pas résoudre sans internet** : la machine doit trouver une résolution compatible dans le lock, sinon errand s'arrête et dit de
  résoudre ailleurs.

➡️ **Recommandé :** (a), résolu par le premier agent qui a internet, avec les paquets virtuels relevés sur la machine qui le demande.

**Réponse :**

Le (b) me paraît moins risqué et va plus dans le sens des caches bien gérés

---

## Q4 : sha256 dans le lock, pas md5

L'ADR 0044 écrit « URL et md5 » pour la partie conda. md5 n'est plus une défense contre une substitution volontaire ( on fabrique des
collisions ). micromamba accepte `url#sha256:…` dans une liste explicite et le vérifie ( testé ) ; repodata donne le sha256 de chaque
paquet. Si la confiance vient du lock, c'est sha256.

Cela change une ligne de l'ADR 0044, que la session qui l'implémente reprendrait ( je ne touche pas à 0044 moi-même : je le lui
signale ).

➡️ **Recommandé :** oui, sha256 partout dans le lock.

**Réponse :**

---

## Q5 : Le trou des paquets déjà extraits

Le cache de paquets est dans le store, partagé entre projets ( ADR 0044 ). Un paquet extrait puis modifié ( à la main, ou par un processus
qui écrit dans son propre répertoire ) serait lié tel quel dans un nouvel env, avec un avertissement que personne ne lit. Options :

- **(a) errand installe avec `--safety-checks enabled --extra-safety-checks`** : chaque fichier est revérifié avant d'être lié ; un
  paquet modifié est refusé, extrait à nouveau depuis son archive ( dont le hash, lui, est vérifié ). Coût : hacher les fichiers à chaque
  construction d'env ( des secondes à une minute pour un env de plusieurs Go ).
- **(b) Laisser le défaut** ( avertissement ) : plus rapide, et le trou reste.

➡️ **Recommandé :** (a), les vérifications complètes à chaque construction.

**Réponse :**

(b)

---

## Q6 : Les images restent hors de l'ADR 0043

L'ADR 0043 d'origine donnait à apptainer un miroir de registre ( donc encore un proxy ). Proposition : **les images n'y passent pas**.
Une image se construit là où il y a internet et se transporte ( `build_on`, déjà là ; ADR 0044 ) ; un `.sif` est un seul fichier, vérifié
par le sha256 que le builder enregistre ( et `apptainer verify` quand il est signé ). Une recette qui part d'une image locale
( `Bootstrap: localimage`, `docker-archive`, `oci-archive` ) se construit sans registre, si un jour il le faut.

➡️ **Recommandé :** oui, les images restent à `build_on`, rien pour elles dans 0043.

**Réponse :**

---
