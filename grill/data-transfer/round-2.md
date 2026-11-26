# errand : données d'une machine à l'autre — tour 2

> Laisse **Réponse** vide pour accepter la recommandation. `?` demande plus de détails.

Tour 1 réglé ( [decisions.md](decisions.md) ). Pour ta remarque sur les systèmes de fichiers partagés, errand a déjà de quoi faire : l'ADR 0028 identifie un répertoire par un fichier id lu dedans, et regroupe les montages. Par exemple, `/workdir2` de cinaps vu depuis node14 compte comme un seul système de fichiers.

## Q7 : Que nomme l'adresse : une machine, un environnement, ou un système de fichiers ?

`errand://lmo/data/petsc-systems` nomme une machine. Mais `/workdir2/...` est le même chemin sur cinaps, node14, node15… et la source peut être sur un disque partagé.

- **a.** un nom de **machine** ( celui d'`errand machines`, ou l'hôte d'un `Ssh` ), suivi d'un chemin absolu sur cette machine. errand en déduit le système de fichiers ( id ADR 0028 ) ;
- **b.** un nom d'**environnement** ( `lmo-jax` ) : plus familier, mais plusieurs environnements partagent une machine, et une image en a un à elle ;
- **c.** l'**id** du système de fichiers : exact, mais illisible pour un humain.

➡️ **Recommandé :** a pour écrire. errand résout l'adresse en `( id du système de fichiers, chemin dans ce système )`, et c'est cette forme qu'il garde ( journal, cache ).

**Réponse :**

---

## Q8 : Quand la destination voit déjà la source : le chemin d'origine tel quel ?

Si le run tourne sur une machine qui voit le même système de fichiers ( même id ), `download` rend **le chemin de la source**, sans copie ni cache. Conséquences :

- pas de protection en lecture seule : un run qui écrit dedans modifie l'original ( c'est déjà le cas si on l'utilise directement ) ;
- l'empreinte : on peut sauter la vérification. Sinon un `sha256 = …` demandé et différent donne une erreur, rien de plus.

➡️ **Recommandé :** oui, le chemin d'origine, sans copie. Le `sha256` est vérifié seulement s'il a été donné.

**Réponse :**

---

## Q9 : Où sont rangées les copies ?

Aujourd'hui `errand-cache/downloads/` est une **zone cache du projet**. La synchro la pousse à **chaque** machine du projet ; avec 1,9 Go ( ou 1 To ), ce n'est plus tenable.

- **a.** les copies vont dans un cache **par système de fichiers**, hors projet ( à côté de `$ERRAND_CACHES`, visible dans `storage`, purgé ). Rien n'est poussé : on ne copie qu'à la demande, vers là où le run tourne ;
- **b.** elles restent dans la zone du projet, comme aujourd'hui.

Les deux systèmes ont une différence :

- avec **a**, un nœud sans internet récupère le fichier parce que **le run le demande** ( le mécanisme « demander à qui a la route » existe déjà ) ;
- avec **b**, le push l'apporte avant même qu'on le demande.

➡️ **Recommandé :** a pour tout, URLs http comprises : un seul endroit, une seule règle. La zone `errand-cache/downloads` du projet n'est plus remplie ; ce qu'elle contient déjà reste lu.

**Réponse :**

---

## Q10 : Combien de temps garder une copie inutilisée ?

Les versions de services et les build dirs ( ADR 0028/0039 ) : **14 jours** sans usage, puis purge. Une copie peut toujours être refaite, si sa source existe encore.

➡️ **Recommandé :** 14 jours sans usage, comme le reste. Elle reste supprimable à la main dans `storage`.

**Réponse :**

Je n'aime pas les trucs qui se font "dans le dos" des utilisateurs. Je préfère que ça se règle à la main, par exemple dans l'interface web ou en cli.

---

## Q11 : Le nom dans l'API

`errand.download( "errand://lmo/data/x" )` se lit mal pour une donnée qui ne vient pas d'internet.

- **a.** garder `download` : un seul nom, rien à apprendre ;
- **b.** un nouveau nom, par exemple `errand.data( adresse )` ( `errand.fetch`, `errand.get`… ), avec `download` gardé comme ancien nom ;
- et pour la ligne de commande, `errand run --needs ADRESSE` ( ou `--data` ) ?

➡️ **Recommandé :** b. `errand.data( adresse )`, et `--data ADRESSE` au lancement ( répétable ). `download( url )` reste un alias.

**Réponse :**

J'ai une préférence pour fetch, mais ça ne dit pas ce qu'il se passe si la donnée est disponible en local. Que dis-tu de `data_url` ?

---
