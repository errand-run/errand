# errand : données d'une machine à l'autre — décisions

- **Catégorie** : une seule notion, une donnée à une adresse ( généralisation de `download` ) ; la copie locale est du cache. Les données non reproductibles sont hors sujet.
- **Identité** : empreinte sha256 calculée une fois à la source par son agent, gardée avec taille et date ; copie rangée sous l'empreinte.
- **Granularité** : fichiers et répertoires ( empreinte = manifeste ) ; envoi fichier par fichier, la copie n'apparaît sous son nom qu'une fois complète.
- **Après la copie** : errand gère la copie ( storage, purge si inutilisée, lecture seule ), jamais la source.
- **Transport** : direct de machine à machine quand c'est possible, sinon par le Mac ; transfert possédé par les agents, visible, repris au réveil ; reprise par blocs.
- **Déclenchement** : paresseux depuis le code et déclaré au lancement ( copie pendant l'attente du run ), même mécanisme ; commande à part plus tard.
- **Noms** : `errand.local_path( adresse ) -> Path` ( `download` gardé comme alias ), `errand run --data ADRESSE` ( répétable ).
- **Système de fichiers partagé** ( Hugo ) : si la destination voit déjà la source, pas de copie.
- **Adresse** : machine + chemin absolu pour écrire ; errand résout en ( id du système de fichiers ADR 0028, chemin ) et garde cette forme.
- **Système de fichiers partagé** : le chemin de la source tel quel, sans copie ; `sha256` vérifié seulement s'il est donné.
- **Où vont les copies** : un cache par système de fichiers, hors projet, visible dans `storage` ; rien n'est poussé, copie à la demande ; vaut aussi pour http. La zone `errand-cache/downloads` n'est plus remplie, son contenu reste lu.
- **Purge** : jamais automatique. Elle se fait à la main ( web, cli ) ; Hugo n'aime pas ce qui se fait dans le dos de l'utilisateur.
- **Purges existantes** ( ADR 0020/0022/0028/0039 ) : errand propose, l'utilisateur décide, partout ; taille et dernier usage affichés. Exception : les entrées **mortes** ( qui ne peuvent plus servir à personne ) restent purgées automatiquement.
- **Source injoignable ou disparue** : on sert la dernière copie connue et on le dit ; `result.json` garde l'empreinte servie.
- **Source modifiée pendant la copie** : l'agent source revérifie taille et date à la fin et renvoie le fichier, trois essais, puis échec nommé ; aucun verrou.
- **Protocoles** : d'abord `errand://`, `http(s)://`, `file://` ; plus tard `s3://`, `ssh://`, `git+https://` derrière une petite interface ; identifiants pris dans la configuration de chaque outil.
- **Étapes** : 1 transfert d'agent à agent possédé et repris par blocs ; 2 `local_path( "errand://…" )`, système partagé, cache par système de fichiers, `storage` ; 3 `--data` ; 4 http dans le nouveau cache ; 5 purges à la main.
