# errand : données d'une machine à l'autre — tour 3

> Laisse **Réponse** vide pour accepter la recommandation. `?` demande plus de détails.

## Q11 bis : Le nom

`data_url` donne envie de croire que la fonction **rend** une URL, alors qu'elle rend un **chemin local**, à partir d'une adresse. Et ton point sur `fetch` est juste : le nom doit dire « ici », que la donnée y soit déjà ou non. Pistes :

| nom | se lit | contre |
|---|---|---|
| `errand.local( adresse )` | le chemin local de cette donnée | un peu court |
| `errand.here( adresse )` | cette donnée, ici | idem |
| `errand.local_path( adresse )` | le plus explicite | long |
| `errand.data( adresse )` | une donnée | ne dit pas « ici » |
| `errand.data_url( adresse )` | ta proposition | suggère une URL en retour |

Côté ligne de commande, il faut le même mot : `errand run --data ADRESSE`, ou `--local ADRESSE`, …

➡️ **Recommandé :** `errand.local_path( adresse ) -> Path` en Python, et `errand run --data ADRESSE` au lancement. Le flag déclare une donnée, la fonction dit où elle est ici.

**Réponse :**

Oui pour local_path

---

## Q12 : Et les purges automatiques qui existent déjà ?

Ta réponse à Q10 va plus loin que les copies. errand purge aujourd'hui **tout seul** :

- les **versions de services** et les **build dirs** inutilisés depuis 14 jours ( ADR 0028/0039 ) : objets compilés, `incremental/` ;
- les **caches de bibliothèques** au format `errand-cache/1` : entrées mortes, au plus une fois par jour ( ADR 0020/0022 ) ;
- `errand caches hold MACHINE DIR` : purge quotidienne par l'agent.

Options :

- **a.** même règle partout : errand **propose** ( « 4,2 Go inutilisés depuis 30 jours » ), l'utilisateur décide ;
- **b.** garder les purges existantes, et laisser seulement les copies de données à la main.

Il y a des cas intermédiaires : une entrée **morte** ( ce dont elle dépend a disparu, elle ne peut plus servir ) n'est pas la même chose qu'une entrée **inutilisée**.

➡️ **Recommandé :** a, avec une exception : la purge des entrées mortes reste automatique ( elles ne peuvent plus servir à personne ). Tout ce qui est seulement inutilisé passe à la main, avec la taille et la date du dernier usage affichées. À confirmer, car cela change ADR 0020/0022/0028/0039.

**Réponse :**

Oui pour a

---

## Q13 : Source injoignable, ou disparue

lmo est éteint, ou `/data/petsc-systems` a été supprimé, et un run demande la donnée. Une copie de la dernière version connue existe là où il tourne.

- **a.** on sert la copie connue et on le dit ( « source injoignable : copie du 25/11, empreinte … » ). Ça fonctionne hors ligne ;
- **b.** erreur : la source fait foi.

Si le run a donné un `sha256`, la question ne se pose pas : la copie qui a cette empreinte convient.

➡️ **Recommandé :** a, en l'écrivant dans le journal du run ( `result.json` garde l'empreinte servie, pour pouvoir comparer deux runs ).

**Réponse :**

---

## Q14 : La source change pendant la copie

Un run réécrit encore `lmo:/data/petsc-systems` pendant qu'un autre le copie.

➡️ **Recommandé :** l'agent de la source relit taille et date de chaque fichier à la fin de son envoi. Si elles ont changé, il recommence ce fichier. Si un fichier change encore après trois essais, la copie échoue et dit lequel. Pas de verrou sur la source : elle n'est pas à errand.

**Réponse :**

---

## Q15 : Quels protocoles dans un premier temps ?

Tu as cité http « et autres ». Chaque protocole demande ses identifiants ( s3 : clés, `ssh:` vers une machine qui n'est pas à errand : clés ssh ).

- **Premier jet** : les chemins des machines d'errand ( `errand://` ), `http(s)://` ( existe déjà ), `file://`.
- **Plus tard** : `s3://`, `ssh://user@hôte/...` pour une machine sans agent, `git+https://…@commit`. Une petite interface « protocole » ( lister, empreinte, lire par blocs ) les accueillera.

➡️ **Recommandé :** ce découpage. Les identifiants viennent de la configuration habituelle de chaque outil ( `~/.ssh/config`, `~/.aws` ), jamais d'errand.

**Réponse :**

---

## Q16 : Dans quel ordre l'écrire ?

Proposition d'étapes pour l'ADR :

1. le transfert d'agent à agent, possédé, repris par blocs. Il améliore aussi les push et pull actuels ;
2. `local_path( "errand://…" )` + système de fichiers partagé + cache par système de fichiers + `storage` ;
3. `--data` au lancement ( copie pendant l'attente ) ;
4. http passé au nouveau cache, ancienne zone en lecture ;
5. purges à la main ( Q12 ).

➡️ **Recommandé :** cet ordre. L'étape 1 seule aurait évité l'incident d'hier.

**Réponse :**

---
