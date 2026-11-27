# ADR 0043 repris : round 3, résoudre sur place avec des index apportés

> Laisse **Réponse** vide pour accepter la recommandation. `?` demande plus de détails.
> Écris n'importe quoi d'autre (réponse, objection, commentaire) sous une question, puis Envoyer.

Réglé : [decisions.md](decisions.md). Tu as choisi (b) : le solveur tourne sur la machine qui le demande, sur des index qu'errand apporte.

**Faits vérifiés** ( 2026-11-27 ) :

- Les index complets de conda-forge pèsent, **compressés**, 61 Mo pour `linux-64` et 30 Mo pour `noarch` ( `repodata.json.zst` ), bien plus
  une fois décompressés.
- conda-forge publie aussi des **index découpés par paquet** ( « sharded repodata » ) : un sommaire de 580 Ko
  ( `repodata_shards.msgpack.zst` ), puis un petit fichier par nom de paquet, **nommé par son sha256**. micromamba 2.10 ne sait pas les lire
  lui-même ; il lit en revanche n'importe quel canal local `file://` muni d'un `repodata.json`.
- **uv résout hors ligne sur un index local** `--index-url file:///…/simple` ( testé : `uv pip compile --offline --generate-hashes
  --python-platform … --python-version …` ). Mais sur un index `file://` il **ouvre la roue elle-même** pour lire ses métadonnées : le seul
  fichier `.metadata` ( PEP 658 ) ne lui suffit pas. Il échoue alors en nommant précisément ce qui manque ( « requests was not found »,
  « failed to open file …/six-1.16.0-py2.py3-none-any.whl » ).

---

## Q1 : L'outil ne touche jamais le réseau, même pour résoudre

Le round 2 garde le réseau de l'outil pour résoudre sur une machine connectée. Avec ton choix (b), le plus simple et le plus cohérent est
qu'**il n'y ait qu'un chemin, partout** : errand apporte les index comme les fichiers ( TLS vérifié par errand jusqu'à l'origine, cache par
machine, relais par les agents ), et micromamba comme uv résolvent et installent **hors ligne**, sur des canaux et index `file://`. La
chaîne de confiance devient : l'index, vérifié par TLS chez errand → les sha256 qu'il donne → le lock → chaque fichier. La question du TLS
des outils disparaît : ils ne parlent plus à personne.

➡️ **Recommandé :** oui, l'outil ne touche jamais le réseau ; index et fichiers passent tous par errand.

**Réponse :**

Oui c'est ça. Rq: tu me parles de l'index comme étant un coût supplémentaire, mais il doit être chargé quelque part par micromamba pour résoudre les paquets même en direct avec internet, non ?

---

## Q2 : Côté conda, les index découpés

- errand lit le sommaire des index découpés, puis va chercher **le fichier de chaque paquet demandé, puis de ses dépendances, de proche en
  proche** ( toutes versions confondues, ce qui sur-approxime un peu ). Il en fait un **canal local réduit** ( un `repodata.json` qui ne
  contient que ces paquets ), sur lequel micromamba résout hors ligne. Quelques Mo au lieu de 90, et tout est adressé par sha256, donc se
  garde bien en cache.
- Le sommaire ( 580 Ko ) est revérifié après 1 heure ; un fichier de paquet, nommé par son hash, ne l'est jamais.
- Un canal qui ne publie pas d'index découpés est apporté **en entier** ( `repodata.json.zst` ), revérifié après 1 heure.
- Sans internet nulle part sur la chaîne, le dernier sommaire connu sert, et la résolution le dit.

➡️ **Recommandé :** oui, canal local réduit construit à partir des index découpés ; index entier en repli.

**Réponse :**

---

## Q3 : Côté PyPI

uv a besoin des **roues elles-mêmes** sur un index `file://`. Trois façons :

- **(a) Une boucle sur ce qu'uv dit manquer, avec un coup d'avance.** errand lance la résolution hors ligne ; uv échoue en nommant le projet
  ou la roue qui manque ; errand va le chercher ( page du projet, ou roue, vérifiée par le sha256 de la page ) et relance. Pour éviter un
  aller-retour par dépendance, errand prend **d'avance** les pages des dépendances déclarées ( `Requires-Dist` ) de chaque roue qu'il
  apporte. Les roues apportées sont, pour l'essentiel, celles qui seront installées : rien de perdu. Le message d'uv est lu par errand, donc
  **la version d'uv est épinglée** ( comme celle de micromamba ), et un message non reconnu arrête tout en le montrant.
- **(b) Des roues « factices »** : pour chaque version, errand fabrique une roue qui ne contient que ses métadonnées ( prises dans le
  `.metadata` de PyPI ), uv résout dessus, puis les vraies roues sont apportées pour installer. Pas de boucle, mais une astuce propre à
  errand, et toutes les versions des dépendances, de proche en proche, sont à parcourir.
- **(c) Un index local qu'uv lit en HTTP** : écarté au round 1 ( HTTP en clair sur 127.0.0.1 ).

➡️ **Recommandé :** (a), la boucle avec un coup d'avance, uv épinglé.

**Réponse :**

---
