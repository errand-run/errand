# errand : données d'une machine à l'autre — tour 1

> Laisse **Réponse** vide pour accepter la recommandation. `?` demande plus de détails.
> Écris n'importe quoi d'autre (réponse, objection, commentaire) sous une question, puis Envoyer.

Point de départ : `errand.download( url, sha256 = … )` existe déjà. Il range le fichier dans `errand-cache/downloads/<sha256>/<nom>`, sur chaque machine, et un nœud sans internet demande à qui a la route. Le cas PETSc ( 1,9 Go écrits par un run dans `lmo:/data/petsc-systems`, lus par un run sur node14 ) ressemble beaucoup à ça, avec une autre source qu'une URL http.

## Q1 : Une nouvelle catégorie, ou une généralisation de `download` ?

Deux façons de voir :

- **A. Une seule notion, « une donnée à une adresse »** : `download` accepte d'autres adresses que http(s), par exemple `errand://lmo/data/petsc-systems` ( un chemin sur le système de fichiers d'une machine qu'errand connaît ), `file:`, `s3:`, `ssh:`… La copie locale reste du cache : on peut la refaire depuis la source, donc la perdre ne coûte que du temps.
- **B. Une nouvelle catégorie, « les données du projet »** : des données qui ne sont pas reproductibles (mesures, la seule copie d'un résultat), déclarées et suivies par errand comme telles.

Le cas PETSc, comme ton exemple, entre dans A. B pose une autre question, celle de la garde d'un original. Celle-là relève de `storage`/ADR 0028, pas du transfert.

➡️ **Recommandé :** A. Une seule notion : une adresse, et une copie locale qui est du cache. B est hors de ce sujet, à reprendre plus tard s'il se présente.

**Réponse :**

---

## Q2 : Comment reconnaître que la copie est encore la bonne ?

Une URL http n'a pas d'identité stable sans `sha256`. Un chemin sur une machine change quand un run le réécrit. Options :

- **a.** l'empreinte du contenu ( sha256 ), calculée **une fois, à la source**, par l'agent de la machine qui détient la donnée, puis gardée tant que taille et date ne changent pas : la copie est rangée sous cette empreinte, comme aujourd'hui ;
- **b.** taille et date seulement : rapide, mais deux machines ne sauraient pas qu'elles ont les mêmes octets ;
- **c.** rien : la première copie fait foi.

Hacher 1,9 Go prend quelques secondes ; on ne le fait qu'une fois par version.

➡️ **Recommandé :** a. L'empreinte, calculée à la source par l'agent et gardée avec taille et date. Si la source change, sa nouvelle version a une nouvelle empreinte, et l'ancienne copie reste valable pour qui l'a demandée par empreinte.

**Réponse :**

---

## Q3 : Un fichier, ou aussi un répertoire ?

`petsc-systems` contient 41 fichiers. Un répertoire peut avoir une empreinte faite du manifeste de ses fichiers ( noms, tailles, empreintes ), le même que celui de la synchro du projet.

➡️ **Recommandé :** les deux. Un répertoire est envoyé fichier par fichier : ce qui est déjà arrivé n'est pas renvoyé, et la copie n'apparaît sous son nom qu'une fois complète.

**Réponse :**

---

## Q4 : Qu'est-ce qu'errand gère après la copie ?

- **La source** : rien. Elle n'est pas à errand : il ne la déplace pas, ne la supprime pas et ne la change pas.
- **La copie** : c'est une entrée de cache comme une autre. Elle est visible dans l'onglet storage, on peut la supprimer, et elle est purgée si personne ne s'en sert depuis N jours ( comme les caches de bibliothèques, ADR 0020/0022 ). Le run qui la demande la retrouve ou la refait venir.
- Elle reste en **lecture seule** : un run qui veut la modifier en fait sa copie à lui.

➡️ **Recommandé :** c'est ça. errand gère le cycle de vie de la copie, jamais celui de la source.

**Réponse :**

---

## Q5 : Par où passent les octets, et que se passe-t-il quand ça coupe ?

Ce qui est arrivé hier : lmo → Mac → node14, Mac en veille, rien n'a repris.

- **Route** : directement d'une machine à l'autre quand l'une peut joindre l'autre ( lmo joint cinaps ; des clés ou un relais d'agent à agent ), sinon par le Mac en dernier recours.
- **Propriétaire** : le transfert appartient aux agents, comme un run ( ADR 0035 ). Il est visible dans `errand status`, survit à la fermeture du portable et reprend au réveil.
- **Reprise** : par blocs vérifiés. Un fichier de 200 Mo coupé à 150 Mo ne recommence pas à zéro.

➡️ **Recommandé :** les trois. Le portable n'est jamais un passage obligé ; s'il l'est, la copie reprend seule au réveil.

**Réponse :**

---

## Q6 : Qui déclenche la copie, et quand ?

- **a. Paresseux, depuis le code** : `errand.download( "errand://lmo/data/petsc-systems" )` dans le run. Simple, mais le run attend la copie en tenant ses cœurs ( avec `-x`, toute la machine ).
- **b. Déclaré au lancement** : `errand run --needs errand://lmo/... …`, ou dans l'entrée. La copie se fait **pendant que le run attend son tour**, sans rien réserver, puis le run démarre avec la donnée déjà là.
- **c. Commande à part** : `errand fetch ADRESSE --to n14`, pour préparer à la main.

➡️ **Recommandé :** a et b, par le même mécanisme. a reste toujours possible ; b sert à ne pas payer la copie sur le temps de la réservation. c peut venir ensuite, comme simple façade.

**Réponse :**

Très bien.
Au passage : il se peut que les données soient accessibles depuis différents environnements s'ils se partagent le même système de fichier. Il faudrait le repérer e éviter les copies dans ce cas (a priori, on a les infos).

---
